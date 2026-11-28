package codex

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var errMailUnavailable = errors.New("個人メールの接続先か検索結果を確かめられませんでした")

// LookupMail uses an ephemeral tool-only thread. No model turn is started;
// neither the normal conversation nor the caller receives the Gmail payload.
func (b *Bridge) LookupMail(ctx context.Context, account, query string) (bool, error) {
	if err := b.connect(ctx); err != nil {
		return false, errMailUnavailable
	}
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return false, errMailUnavailable
	}
	return lookupMail(ctx, c.call, b.cfg.Workspace, account, query)
}

func lookupMail(ctx context.Context, rpc func(context.Context, string, any, any) error, workspace, account, query string) (bool, error) {
	var started struct {
		Thread struct{ ID string } `json:"thread"`
	}
	settings := map[string]any{"cwd": workspace, "ephemeral": true, "approvalPolicy": "never", "sandbox": "read-only"}
	if err := rpc(ctx, "thread/start", settings, &started); err != nil || started.Thread.ID == "" {
		return false, errMailUnavailable
	}
	defer func() {
		finish, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rpc(finish, "thread/unsubscribe", map[string]string{"threadId": started.Thread.ID}, nil)
	}()
	var inventory struct {
		Data []struct {
			Name  string                     `json:"name"`
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"data"`
	}
	const server = "codex_apps"
	list := map[string]any{"threadId": started.Thread.ID, "serverName": server}
	if err := rpc(ctx, "mcpServerStatus/list", list, &inventory); err != nil {
		return false, errMailUnavailable
	}
	var ready bool
	for _, item := range inventory.Data {
		ready = ready || item.Name == server && item.Tools["gmail.get_profile"] != nil && item.Tools["gmail.search_email_ids"] != nil
	}
	if !ready {
		return false, errMailUnavailable
	}
	call := func(tool string, args map[string]any, out any) error {
		params := map[string]any{"threadId": started.Thread.ID, "server": server, "tool": tool, "arguments": args}
		var result struct {
			Failed bool            `json:"isError"`
			Data   json.RawMessage `json:"structuredContent"`
		}
		if err := rpc(ctx, "mcpServer/tool/call", params, &result); err != nil || result.Failed {
			return errMailUnavailable
		}
		if err := json.Unmarshal(result.Data, out); err != nil {
			return errMailUnavailable
		}
		return nil
	}
	var profile struct{ Email string }
	if err := call("gmail.get_profile", map[string]any{}, &profile); err != nil || profile.Email != account {
		return false, errMailUnavailable
	}
	var matches struct {
		IDs []string `json:"message_ids"`
	}
	if err := call("gmail.search_email_ids", map[string]any{"query": query, "max_results": 1}, &matches); err != nil || matches.IDs == nil {
		return false, errMailUnavailable
	}
	return len(matches.IDs) > 0, nil
}
