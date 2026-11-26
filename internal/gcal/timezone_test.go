package gcal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imos/lacia/internal/calendar"
)

func TestFetcherUsesConfiguredRangeAndMetadata(t *testing.T) {
	fail := false
	srv := fakeGoogle(t, &fail)
	defer srv.Close()
	client := &Client{Endpoints: Endpoints{Auth: srv.URL + "/auth", Token: srv.URL + "/token", API: srv.URL + "/api"}}
	root := t.TempDir()
	store := &Store{Path: filepath.Join(root, "credentials.json")}
	for i, id := range []string{"work", "home"} {
		cred, _, err := client.Exchange(context.Background(), id, "secret", "http://x/cb", "good-code", "verifier")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Put(calendar.Scope[i].Account, cred); err != nil {
			t.Fatal(err)
		}
	}
	loc, _ := time.LoadLocation("America/New_York")
	f := &Fetcher{Store: store, Client: client, Location: loc, Path: filepath.Join(root, "snapshot.json"), Now: func() time.Time { return time.Date(2026, 11, 12, 0, 30, 0, 0, time.UTC) }}
	if err := f.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Path)
	var snapshot map[string]any
	json.Unmarshal(data, &snapshot)
	if snapshot["timezone"] != "America/New_York" || snapshot["range_start"] != "2026-11-11" {
		t.Fatal(snapshot["timezone"], snapshot["range_start"])
	}
	if _, err := calendar.Parse(data); err != nil {
		t.Fatal(err)
	}
}
