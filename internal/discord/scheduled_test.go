package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScheduledDestination(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		registered  bool
		all         bool
		destination Channel
		getStatus   int
		postStatus  int
		wantCalls   string
		wantError   bool
	}{
		{name: "registered channel", id: "100", registered: true, destination: Channel{ID: "100", GuildID: "90"}, wantCalls: "GET POST"},
		{name: "alternate channel explicitly enabled", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90"}, wantCalls: "GET POST"},
		{name: "voice text chat", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90", Type: 2}, wantCalls: "GET POST"},
		{name: "announcement", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90", Type: 5}, wantCalls: "GET POST"},
		{name: "stage text chat", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90", Type: 13}, wantCalls: "GET POST"},
		{name: "alternate channel disabled", id: "100", wantError: true},
		{name: "malformed ID", id: "100?x=1", all: true, wantError: true},
		{name: "other guild even if registered", id: "100", registered: true, all: true, destination: Channel{ID: "100", GuildID: "99"}, wantCalls: "GET", wantError: true},
		{name: "direct message", id: "100", all: true, destination: Channel{ID: "100", Type: 1}, wantCalls: "GET", wantError: true},
		{name: "inconsistent ID", id: "100", all: true, destination: Channel{ID: "101", GuildID: "90"}, wantCalls: "GET", wantError: true},
		{name: "public thread", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90", Type: 11}, wantCalls: "GET", wantError: true},
		{name: "private thread", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90", Type: 12}, wantCalls: "GET", wantError: true},
		{name: "forum parent", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90", Type: 15}, wantCalls: "GET", wantError: true},
		{name: "not viewable", id: "100", all: true, getStatus: http.StatusForbidden, wantCalls: "GET", wantError: true},
		{name: "visible but cannot post", id: "100", all: true, destination: Channel{ID: "100", GuildID: "90"}, postStatus: http.StatusForbidden, wantCalls: "GET POST", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := make(chan string, 5)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/channels/100":
					calls <- r.Method
					if test.getStatus != 0 {
						w.WriteHeader(test.getStatus)
						return
					}
					_ = json.NewEncoder(w).Encode(test.destination)
				case r.Method == http.MethodPost && r.URL.Path == "/channels/100/messages":
					calls <- r.Method
					if test.postStatus != 0 {
						w.WriteHeader(test.postStatus)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Discord call %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			config := Config{Token: "fixture", GuildID: "90", MentionAllChannels: test.all, api: server.URL}
			if test.registered {
				config.Channels = []string{test.id}
			}
			err := New(config).SendScheduled(context.Background(), test.id, "fixture", "架空の定期投稿です。")
			if (err != nil) != test.wantError {
				t.Errorf("error = %v, want error: %v", err, test.wantError)
			}
			close(calls)
			var recorded []string
			for call := range calls {
				recorded = append(recorded, call)
			}
			if got := strings.Join(recorded, " "); got != test.wantCalls {
				t.Errorf("calls = %q, want %q", got, test.wantCalls)
			}
		})
	}
}
