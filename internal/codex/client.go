// Package codex talks to a long-running `codex app-server` over its
// JSON-RPC WebSocket, and keeps one conversation open on it.
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/imos/lacia/internal/effect/network"
)

// client is a minimal JSON-RPC peer: numbered requests, and a callback for
// everything the server sends on its own.
type client struct {
	conn    *websocket.Conn
	next    atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan response
	closed  chan struct{}
	err     error
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type incoming struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	response
}

// dial connects to the app-server's Unix socket and starts reading.
// notify is called, in order, for each notification.
func dial(ctx context.Context, socket string, notify func(method string, params json.RawMessage)) (*client, error) {
	conn, err := network.SocketWebSocket(ctx, socket)
	if err != nil {
		return nil, err
	}
	// A resumed thread with a long history arrives as one large message.
	conn.SetReadLimit(256 << 20)
	c := &client{conn: conn, pending: map[int64]chan response{}, closed: make(chan struct{})}
	go c.read(notify)
	return c, nil
}

func (c *client) read(notify func(string, json.RawMessage)) {
	defer close(c.closed)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var msg incoming
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch {
		case msg.Method != "" && msg.ID == nil:
			notify(msg.Method, msg.Params)
		case msg.Method != "":
			// A request from the server (an approval prompt, for example).
			// Nothing here can answer one, so say so instead of hanging it.
			c.write(context.Background(), map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "not supported by this client"}})
		case msg.ID != nil:
			var id int64
			if json.Unmarshal(*msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg.response
			}
		}
	}
}

func (c *client) write(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// call sends a request and decodes the result into out (which may be nil).
func (c *client) call(ctx context.Context, method string, params, out any) error {
	id := c.next.Add(1)
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return fmt.Errorf("Codexとの接続が切れました: %w", c.err)
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("%s の処理中に、Codexとの接続が切れました", method)
		}
		if resp.Error != nil {
			return fmt.Errorf("Codexが %s の処理でエラーを返しました: %s", method, resp.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

func (c *client) notifyServer(ctx context.Context, method string) error {
	return c.write(ctx, map[string]any{"method": method})
}

func (c *client) close() {
	c.conn.Close(websocket.StatusNormalClosure, "")
	<-c.closed
}
