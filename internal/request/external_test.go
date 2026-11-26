package request

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestExternalResultNeverRunsAndDeliveryIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{ID: "sched-daily-2026-11-24-0600", Text: "prompt", Source: "予約", ReplyTo: "discord:trusted", Status: Done, Reply: "done", Finished: time.Now().Add(-24 * time.Hour)}
	// Simulate a coordinator stopping after durable storage but before the
	// delivery callback was installed, even beyond startup's two-hour window.
	if err = q.RecordExternal(r); err != nil {
		t.Fatal(err)
	}
	q, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	q.Finished = func(got Request) {
		ok, err := q.ClaimDelivery(got.ID)
		if err != nil {
			t.Error(err)
		}
		if ok {
			sent++
			q.SetDelivery(got.ID, "sent")
		}
	}
	r.ReplyTo = "discord:untrusted"
	r.Reply = "changed"
	if err = q.RecordExternal(r); err != nil {
		t.Fatal(err)
	}
	if err = q.RecordExternal(r); err != nil {
		t.Fatal(err)
	}
	stored, _ := q.Get(r.ID)
	if sent != 1 || stored.Reply != "done" || stored.ReplyTo != "discord:trusted" {
		t.Fatalf("sent=%d stored=%+v", sent, stored)
	}
	q, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	q.Finished = func(Request) { t.Error("already delivered result repeated") }
	if err = q.RecordExternal(r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	go q.Work(ctx, false, func(context.Context, string) (string, string, error) { ran.Store(true); return "", "", nil }, nil)
	time.Sleep(20 * time.Millisecond)
	cancel()
	if ran.Load() {
		t.Fatal("external result queued a conversation")
	}
}
func TestExternalResultDoesNotRetryUnknownDelivery(t *testing.T) {
	dir := t.TempDir()
	q, _ := Open(dir)
	r := Request{ID: "sched-daily-2026-11-24-0600", Status: Done, ReplyTo: "discord:trusted", Finished: time.Now()}
	q.RecordExternal(r)
	if ok, err := q.ClaimDelivery(r.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	q, _ = Open(dir)
	q.Finished = func(Request) { t.Error("unknown POST retried") }
	if err := q.RecordExternal(r); err != nil {
		t.Fatal(err)
	}
}
