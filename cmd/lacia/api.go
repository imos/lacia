package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/effect/network"
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
	resp, err := network.ServerRestarting(timeout).Do(req)
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
	resp, err := network.ServerRestarting(10 * time.Minute).Do(req)
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
