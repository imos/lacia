package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/imos/lacia/internal/effect/network"
)

// intents: guilds, guild messages, message content.
const intents = 1 | 1<<9 | 1<<15

type frame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

// errFatal means reconnecting cannot help (bad token, intents not granted).
var errFatal = errors.New("discord: 接続を続けられません")

// session is the state needed to resume a dropped connection.
type session struct {
	id     string
	resume string
	seq    int64
	hasSeq bool
}

// gateway keeps the event connection alive and reports new messages.
type gateway struct {
	rest      *rest
	token     string
	dialURL   string // overrides the URL from Discord, for tests
	onReady   func(ctx context.Context, botID string, resumed bool)
	onMessage func(ctx context.Context, m Message)
}

// run connects and reconnects until ctx ends or the failure is fatal.
func (g *gateway) run(ctx context.Context) error {
	var s session
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := g.connect(ctx, &s)
		if errors.Is(err, errFatal) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("discord connection ended; reconnecting", "error", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
	return nil
}

func (g *gateway) connect(ctx context.Context, s *session) error {
	target := g.dialURL
	if target == "" {
		target = s.resume
		if target == "" {
			u, err := g.rest.gatewayURL(ctx)
			if err != nil {
				return err
			}
			target = u
		}
		target += "/?v=10&encoding=json"
	}
	// Handling an event already read outlives the connection: the sequence
	// number has moved past it, so a resumed session will not send it again.
	work := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := network.WebSocket(ctx, target)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)

	var writeMu sync.Mutex
	send := func(op int, d any) error {
		data, err := json.Marshal(map[string]any{"op": op, "d": d})
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(ctx, websocket.MessageText, data)
	}
	read := func() (frame, error) {
		var f frame
		_, data, err := conn.Read(ctx)
		if err != nil {
			switch websocket.CloseStatus(err) {
			case 4004, 4010, 4011, 4012, 4013, 4014:
				return f, fmt.Errorf("%w（close %d）", errFatal, websocket.CloseStatus(err))
			}
			return f, err
		}
		return f, json.Unmarshal(data, &f)
	}

	hello, err := read()
	if err != nil {
		return err
	}
	var h struct {
		Interval int `json:"heartbeat_interval"`
	}
	if hello.Op != 10 || json.Unmarshal(hello.D, &h) != nil || h.Interval <= 0 {
		return errors.New("discord: hello が届きませんでした")
	}
	var mu sync.Mutex
	acked := true
	beat := func() error {
		mu.Lock()
		var seq any
		if s.hasSeq {
			seq = s.seq
		}
		mu.Unlock()
		return send(1, seq)
	}
	go func() {
		interval := time.Duration(h.Interval) * time.Millisecond
		timer := time.NewTimer(time.Duration(rand.Float64() * float64(interval)))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			mu.Lock()
			ok := acked
			acked = false
			mu.Unlock()
			if !ok || beat() != nil {
				cancel() // a missed ack means the connection is dead
				return
			}
			timer.Reset(interval)
		}
	}()

	if s.id != "" {
		err = send(6, map[string]any{"token": g.token, "session_id": s.id, "seq": s.seq})
	} else {
		err = send(2, map[string]any{"token": g.token, "intents": intents,
			"properties": map[string]string{"os": "linux", "browser": "lacia", "device": "lacia"}})
	}
	if err != nil {
		return err
	}
	for {
		f, err := read()
		if err != nil {
			return err
		}
		if f.S != nil {
			mu.Lock()
			s.seq, s.hasSeq = *f.S, true
			mu.Unlock()
		}
		switch f.Op {
		case 11:
			mu.Lock()
			acked = true
			mu.Unlock()
		case 1:
			if err := beat(); err != nil {
				return err
			}
		case 7:
			return errors.New("discord: つなぎ直しを求められました")
		case 9:
			var resumable bool
			json.Unmarshal(f.D, &resumable)
			if !resumable {
				*s = session{}
			}
			time.Sleep(time.Duration(1+rand.IntN(4)) * time.Second)
			return errors.New("discord: セッションが無効になりました")
		case 0:
			switch f.T {
			case "READY":
				var ready struct {
					SessionID string `json:"session_id"`
					ResumeURL string `json:"resume_gateway_url"`
					User      struct {
						ID string `json:"id"`
					} `json:"user"`
				}
				if err := json.Unmarshal(f.D, &ready); err != nil {
					return err
				}
				s.id, s.resume = ready.SessionID, ready.ResumeURL
				g.onReady(work, ready.User.ID, false)
			case "RESUMED":
				g.onReady(work, "", true)
			case "MESSAGE_CREATE":
				var m Message
				if json.Unmarshal(f.D, &m) == nil {
					g.onMessage(work, m)
				}
			}
		}
	}
}
