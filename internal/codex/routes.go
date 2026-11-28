package codex

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
)

// Routes are runtime state, kept outside Git. A turn is bound before any
// child notification from it is released, including notifications that race
// the turn/start response. Children keep their origin across later turns.
type childRoutes struct {
	mu       sync.Mutex
	Turns    map[string]string      `json:"turns"`
	Children map[string]childOrigin `json:"children"`
	loaded   bool
	awaiting bool
	pending  []Child
}

type childOrigin struct {
	RequestID string `json:"request_id"`
	TurnID    string `json:"turn_id"`
	Finished  bool   `json:"finished"`
}

func (b *Bridge) loadRoutes() error {
	r := &b.routes
	if r.loaded {
		return nil
	}
	data, err := b.cfg.State.ReadFile("child-routes.json")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, r); err != nil {
			return err
		}
	}
	if r.Turns == nil {
		r.Turns = map[string]string{}
	}
	if r.Children == nil {
		r.Children = map[string]childOrigin{}
	}
	r.loaded = true
	return nil
}

func (b *Bridge) saveRoutes() error {
	data, err := json.Marshal(&b.routes)
	if err != nil {
		return err
	}
	return b.cfg.State.WriteFile("child-routes.json", data, 0o600)
}

func (b *Bridge) awaitingTurn(awaiting bool) {
	b.routes.mu.Lock()
	b.routes.awaiting = awaiting
	b.routes.mu.Unlock()
}

func (b *Bridge) bindTurn(turnID, requestID string) error {
	b.routes.mu.Lock()
	if err := b.loadRoutes(); err != nil {
		b.routes.mu.Unlock()
		return err
	}
	b.routes.Turns[turnID] = requestID
	err := b.saveRoutes()
	b.routes.awaiting = false
	pending := b.routes.pending
	b.routes.pending = nil
	b.routes.mu.Unlock()
	if err != nil {
		return err
	}
	for _, child := range pending {
		b.routeChild(child)
	}
	return nil
}

func (b *Bridge) routeChild(child Child) {
	r := &b.routes
	r.mu.Lock()
	err := b.loadRoutes()
	if err == nil {
		origin, bound := r.Children[child.ThreadID]
		requestID, known := r.Turns[child.TurnID]
		if !known && !bound && r.awaiting {
			r.pending = append(r.pending, child)
			r.mu.Unlock()
			return
		}
		// A reused child starts a new execution; a replay from the same turn
		// keeps its original completion key. Terminal status changes don't
		// create a second completion notification.
		if known && (child.Kind == "started" || child.Kind == "interacted") && (!bound || origin.Finished && origin.TurnID != child.TurnID) {
			origin = childOrigin{RequestID: requestID, TurnID: child.TurnID}
			bound = true
		}
		if bound {
			origin.Finished = origin.Finished || child.Finished()
			r.Children[child.ThreadID] = origin
			err = b.saveRoutes()
			if err == nil {
				child.RequestID, child.OriginTurnID = origin.RequestID, origin.TurnID
			}
		}
	}
	r.mu.Unlock()
	if err != nil {
		slog.Error("child origin save", "child", child.ThreadID, "error", err)
	}
	if b.OnChild != nil {
		b.OnChild(child)
	}
}
