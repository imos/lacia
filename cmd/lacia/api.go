package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/caller"
)

// localAPI is the default loopback port for a read-only mirror that
// uses the notifier's port-forward to a server on another machine.
const localAPI = "http://127.0.0.1:18190"

const apiAddressHelp = "lacia serve の接続先が見つかりません。手元で使うときは lacia serve を起動してください。別のサーバーを使うときは、LACIA_API に接続先の URL を指定してください"

// apiBase resolves an explicit address, a mirror's connection, or the
// address written by lacia serve while it runs against these records.
func apiBase() string {
	root, _ := findRoot("")
	return string(apiForRoot(root))
}

type apiClient string

var apiTransport = sync.OnceValue(func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialWhileRestarting(ctx, dial, network, address, 30*time.Second, 250*time.Millisecond)
	}
	return transport
})

// Retry only refused TCP dials. By staying below HTTP, no request body,
// redirect, TLS handshake, or partially received response is replayed.
func dialWhileRestarting(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), network, address string, within, interval time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	for {
		conn, err := dial(ctx, network, address)
		if !errors.Is(err, syscall.ECONNREFUSED) || network != "tcp" {
			return conn, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, err
		case <-timer.C:
		}
	}
}

// apiForRoot routes operations based on the repository they will affect.
func apiForRoot(root string) apiClient {
	if base := strings.TrimRight(os.Getenv("LACIA_API"), "/"); base != "" {
		return apiClient(base)
	}
	if root != "" && readOnlyCopy(root) {
		return localAPI
	}
	// A server that takes requests in this repository leaves its address.
	if root != "" {
		if note, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(serverNote))); err == nil {
			if base := strings.TrimSpace(string(note)); strings.HasPrefix(base, "http://") {
				return apiClient(base)
			}
		}
	}
	return ""
}

// readOnlyCopy reports whether pushing from the repository is disabled,
// which marks a copy that only the server may change.
func readOnlyCopy(root string) bool {
	config, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	return err == nil && strings.Contains(string(config), "disabled://")
}

// me is the conversation that ran this command, found once.
var me = sync.OnceValue(caller.Detect)

// apiCall sends JSON to the server and decodes the reply into out.
func apiCall(method, path string, in, out any) error {
	return apiClient(apiBase()).call(method, path, in, out)
}

func (api apiClient) call(method, path string, in, out any) error {
	return api.request(method, path, in, out, 30*time.Second)
}

func (api apiClient) readTask(path string, out any) error {
	return api.request(http.MethodGet, path, nil, out, 5*time.Second)
}

type apiStatusError struct {
	code    int
	message string
}

func (err *apiStatusError) Error() string { return err.message }

func (api apiClient) request(method, path string, in, out any, timeout time.Duration) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, string(api)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(caller.Header, me().Encode())
	resp, err := (&http.Client{Timeout: timeout, Transport: apiTransport()}).Do(req)
	if err != nil {
		return api.connectionError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return &apiStatusError{resp.StatusCode, e.Error}
		}
		return &apiStatusError{resp.StatusCode, fmt.Sprintf("サーバーが HTTP %d を返しました", resp.StatusCode)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (api apiClient) connectionError(err error) error {
	if api == localAPI {
		return fmt.Errorf("サーバー（%s）へつながりませんでした。ポート転送を使っているときは、通知アプリが動いているか、接続の認証が有効かを確かめてください。直接つなぐときは LACIA_API に接続先の URL を指定します。\n%w", api, err)
	}
	return fmt.Errorf("サーバー（%s）へつながりませんでした。手元で使うときは、lacia serve が起動しているか、どの URL で待ち受けているかを確かめてください。別のサーバーを使うときは、接続先の URL と、そこまでの通信経路を確かめてください。\n%w", api, err)
}

// put sends a file as the body of a PUT request.
func (api apiClient) put(path string, body io.Reader) error {
	return api.putOut(path, body, nil)
}

// putOut is put that also decodes the reply into out.
func (api apiClient) putOut(path string, body io.Reader, out any) error {
	req, err := http.NewRequest(http.MethodPut, string(api)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(caller.Header, me().Encode())
	resp, err := (&http.Client{Timeout: 10 * time.Minute, Transport: apiTransport()}).Do(req)
	if err != nil {
		return api.connectionError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var out struct{ Error string }
		if json.Unmarshal(data, &out) == nil && out.Error != "" {
			return errors.New(out.Error)
		}
		return fmt.Errorf("サーバーが HTTP %s を返しました", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
