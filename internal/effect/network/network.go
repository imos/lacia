// Package network is the only package in lacia that opens connections. The
// hosts on the internet lacia may reach are listed in hosts.go, and a client
// for them refuses any other. The other connections go where the owner
// said: to a lacia server, or to a socket on this machine.
package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// Internet returns a client for the hosts in hosts.go. A request to any
// other host, by a redirect too, fails before it is sent.
func Internet(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: listedOnly{http.DefaultTransport}}
}

type listedOnly struct{ next http.RoundTripper }

func (l listedOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := check(req.URL); err != nil {
		return nil, err
	}
	return l.next.RoundTrip(req)
}

// WebSocket opens a WebSocket to a host in hosts.go.
func WebSocket(ctx context.Context, target string) (*websocket.Conn, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if err := check(u); err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, target, nil)
	return conn, err
}

// check passes the hosts in hosts.go, and this machine itself, which is not
// outside it: tests stand in for the services there.
func check(u *url.URL) error {
	host := u.Hostname()
	if host == "localhost" || net.ParseIP(host).IsLoopback() || slices.ContainsFunc(hosts, func(h string) bool {
		return host == h || strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)
	}) {
		return nil
	}
	return fmt.Errorf("%s は、lacia が通信してよい先の一覧（internal/effect/network/hosts.go）にありません", host)
}

// Server returns a client for a lacia server the owner named in the
// settings: this machine's own, another profile's, or the one that hands
// out scheduled work. The owner chooses its address, so it is not checked
// against hosts.go.
func Server(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }

// ServerRestarting is Server for a command that may run while the server
// restarts: a refused connection is tried again for 30 seconds.
func ServerRestarting(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: restartingTransport()}
}

var restartingTransport = sync.OnceValue(func() *http.Transport {
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

// Listening reports whether a program on this machine accepts connections
// on the Unix socket at path, waiting a second at most.
func Listening(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err == nil {
		conn.Close()
	}
	return err == nil
}

// SocketWebSocket opens a WebSocket over the Unix socket at path, to a
// program on this machine: the Codex app-server.
func SocketWebSocket(ctx context.Context, socket string) (*websocket.Conn, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	conn, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	return conn, err
}
