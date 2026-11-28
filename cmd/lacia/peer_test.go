package main

import (
	"github.com/imos/lacia/internal/testrecords"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/web"
)

// `lacia peer` and `lacia home`, the earlier name, do the same thing, and
// call the peer profile by the label of the settings.
func TestPeerCommand(t *testing.T) {
	open := func() (*task.Store, *request.Queue) {
		store, err := task.Open(testrecords.Init(t, t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		queue, err := request.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return store, queue
	}
	homeStore, homeQueue := open()
	home, err := (&web.Server{Store: homeStore, Requests: homeQueue, Inbox: &web.Inbox{}, Label: "家庭モード", PeerLabel: "仕事モード"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	homeServer := httptest.NewServer(home)
	defer homeServer.Close()
	workStore, sent := open()
	server := &web.Server{Store: workStore, Peer: &web.Peer{URL: homeServer.URL, Sent: sent}, Label: "仕事モード", PeerLabel: "家庭モード"}
	work, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	workServer := httptest.NewServer(work)
	defer workServer.Close()
	t.Setenv("LACIA_API", workServer.URL)

	if got := peerLabel(); got != "家庭モード" {
		t.Errorf("label of the peer = %q", got)
	}
	const id = "m-from-peer"
	if err := runPeer([]string{"tell", "--id", id, "引き継いだ件は、来週になります。"}); err != nil {
		t.Fatalf("lacia peer tell: %v", err)
	}
	if got, ok := homeQueue.Get("inbox-" + id); !ok || got.From != "仕事モードのレイシア" {
		t.Errorf("lacia peer tell arrived as %+v", got)
	}
	if err := runPeer([]string{"status", id}); err != nil {
		t.Errorf("lacia peer status: %v", err)
	}
	if err := runPeer(nil); err == nil || !strings.HasPrefix(err.Error(), "使い方:\n  lacia peer tell [--task ID]") {
		t.Errorf("usage = %v", err)
	}

	// A server that does not say leaves the general word.
	server.PeerLabel = ""
	if got := peerLabel(); got != self.DefaultPeerLabel {
		t.Errorf("label without settings = %q", got)
	}
}
