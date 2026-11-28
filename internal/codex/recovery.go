package codex

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Maintain reconnects the main conversation even when no new user message
// arrives. The app-server and its agents keep running during a web restart.
func (b *Bridge) Maintain(ctx context.Context) {
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := b.connect(attempt)
		cancel()
		if err != nil {
			slog.Warn("codex reconnect", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

type activityItem struct {
	Type          string `json:"type"`
	Kind          string `json:"kind"`
	AgentThreadID string `json:"agentThreadId"`
	AgentPath     string `json:"agentPath"`
}

type activityTurn struct {
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Items  []activityItem `json:"items"`
}

// recoverChildren reads activity history when there are saved child origins
// or turns to reconcile from a disconnected reader. Unbound child replies are
// never guessed or posted on upgrade.
func (b *Bridge) recoverChildren(ctx context.Context, c *client, threadID string) error {
	b.routes.mu.Lock()
	err := b.loadRoutes()
	haveOrigins := len(b.routes.Turns) > 0
	b.routes.mu.Unlock()
	b.mu.Lock()
	haveTurns := len(b.turns) > 0
	b.mu.Unlock()
	if err != nil || !haveOrigins && !haveTurns {
		return err
	}
	var turns []activityTurn
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{"threadId": threadID, "itemsView": "full", "sortDirection": "asc", "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Data       []activityTurn `json:"data"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := c.call(ctx, "thread/turns/list", params, &result); err != nil {
			return err
		}
		turns = append(turns, result.Data...)
		cursor = result.NextCursor
		if cursor == "" {
			b.replayChildren(turns)
			b.reconcileTurns(turns)
			return nil
		}
	}
	return fmt.Errorf("会話の履歴が100ページを超えたため、サブエージェントの通知の復元を保留しました")
}

// reconcileTurns clears ended turns whose completion was missed on reconnect.
// Live turns remain recorded until the current reader sees their completion.
func (b *Bridge) reconcileTurns(turns []activityTurn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, turn := range turns {
		switch turn.Status {
		case "completed", "interrupted", "failed":
			delete(b.turns, turn.ID)
		case "inProgress":
			b.turn(turn.ID)
		}
	}
}

func (b *Bridge) replayChildren(turns []activityTurn) {
	// Read the latest state first: a previous completion must not finish a
	// reused child that is now running. Bind only children with known origins.
	latest := map[string]Child{}
	origins := map[string]childOrigin{}
	order := map[string]int{}
	for index, turn := range turns {
		order[turn.ID] = index
		for _, item := range turn.Items {
			if item.Type != "subAgentActivity" || item.AgentThreadID == "" {
				continue
			}
			child := Child{ThreadID: item.AgentThreadID, TurnID: turn.ID, Kind: item.Kind, Path: item.AgentPath}
			b.routes.mu.Lock()
			requestID, known := b.routes.Turns[turn.ID]
			b.routes.mu.Unlock()
			if known && (child.Kind == "started" || child.Kind == "interacted") {
				origins[child.ThreadID] = childOrigin{RequestID: requestID, TurnID: turn.ID}
			}
			latest[child.ThreadID] = child
		}
	}
	for id, child := range latest {
		b.routes.mu.Lock()
		current, bound := b.routes.Children[id]
		origin, known := origins[id]
		currentIndex, currentInHistory := order[current.TurnID]
		newerExecution := bound && current.Finished && currentInHistory && order[origin.TurnID] > currentIndex
		if known && (!bound || newerExecution) {
			b.routes.Children[id] = origin
			if err := b.saveRoutes(); err != nil {
				slog.Error("child history origin save", "child", id, "error", err)
				delete(b.routes.Children, id)
			} else {
				bound = true
			}
		}
		b.routes.mu.Unlock()
		if bound && child.Finished() {
			b.routeChild(child)
		}
	}
}
