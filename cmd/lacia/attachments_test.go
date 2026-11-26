package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscordAttachCLI(t *testing.T) {
	calls := 0
	status := "sent"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/discord/attachments" {
			t.Error("wrong endpoint")
		}
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in["request_id"] != "original" || in["path"] != "exports/out.pdf" {
			t.Errorf("input=%v", in)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": status, "filename": "out.pdf", "message_id": "message"})
	}))
	defer srv.Close()
	t.Setenv("LACIA_API", srv.URL)
	args := []string{"attach", "--request", "original", "--file", "exports/out.pdf"}
	if err := runDiscord(args); err != nil {
		t.Fatal(err)
	}
	status = "unknown"
	if err := runDiscord(args); err == nil {
		t.Fatal("unknown shown as success")
	}
	for _, args := range [][]string{{"attach"}, {"attach", "--request", "original"}, {"attach", "--file", "out.pdf"}, {"attach", "--request", "original", "--file", "out.pdf", "unexpected"}} {
		if err := runDiscord(args); err == nil {
			t.Fatal("accepted missing arguments")
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
