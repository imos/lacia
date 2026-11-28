package request

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResumePersistsSingleSuccessorAndContext(t *testing.T) {
	dir := t.TempDir()
	q, _ := Open(dir)
	original, _, _ := q.Add(Request{ID: "original-request", TaskID: "T1001", Text: "private-original"})
	q.Resolve(original.ID, Running, "途中の返事", "", false)
	q, _ = Open(dir)
	original, _ = q.Get(original.ID)
	if original.Status != Unknown || q.Pending() != 0 {
		t.Fatal("restart replayed running request")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := q.Resume(original.ID, "", "Web", "review"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if q.Pending() != 1 {
		t.Fatal("duplicate resume")
	}
	q, _ = Open(dir)
	successor, created, err := q.Resume(original.ID, "another-id", "Web", "")
	if err != nil || created || successor.ResumeOf != original.ID || successor.Text != original.Text {
		t.Fatalf("lost successor: %+v %v", successor, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Finished = func(Request) { cancel() }
	q.Work(ctx, false, func(ctx context.Context, prompt string) (string, string, error) {
		for _, part := range []string{original.ID, "途中の返事", "外部への書き込み", "確かめられなければ、ご本人に尋ねてください", "目的、完了条件、許可の範囲", "private-original"} {
			if !strings.Contains(prompt, part) {
				t.Errorf("missing recovery context: %s", part)
			}
		}
		return "続けました", "turn", nil
	}, nil)
	// Reposting the original recovery does not start work again after completion.
	if _, created, err := q.Resume(original.ID, "", "Web", ""); err != nil || created || q.Pending() != 0 {
		t.Fatal("completed successor repeated")
	}
}

func TestResumeRejectsUnsafeRequests(t *testing.T) {
	base := Request{ID: "original-request", Kind: User, TaskID: "T1001", Text: "request", Status: Failed}
	cases := map[string]func(*Request){
		"system": func(r *Request) { r.Kind = System }, "running": func(r *Request) { r.Status = Running }, "queued": func(r *Request) { r.Status = Queued }, "done": func(r *Request) { r.Status = Done }, "keep": func(r *Request) { r.StatusPolicy = KeepStatus }, "no-task": func(r *Request) { r.TaskID = "" }, "from": func(r *Request) { r.From = "other" }, "author": func(r *Request) { r.Author = "discord:other" }, "route": func(r *Request) { r.ReplyTo = "discord:channel" }, "parent": func(r *Request) { r.ParentID = "parent" }, "side": func(r *Request) { r.Side = true }, "delivery": func(r *Request) { r.Delivery = "failed" }, "channel": func(r *Request) { r.DiscordSourceChannel = "channel" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := base
			change(&r)
			if r.CanResume() {
				t.Fatal("unsafe recovery allowed")
			}
		})
	}
	q, _ := Open(t.TempDir())
	q.Add(base)
	q.Resolve(base.ID, Failed, "", "failure", false)
	q.Add(Request{ID: "task-in-flight", TaskID: "T1001", Text: "new request"})
	if _, _, err := q.Resume(base.ID, "", "Web", ""); err == nil {
		t.Fatal("overlapped task work")
	}
}

func TestWorkDoesNotRunWithoutDurableRunningState(t *testing.T) {
	dir := t.TempDir()
	q, _ := Open(dir)
	saved, _, _ := q.Add(Request{ID: "save-failure", TaskID: "T1001", Text: "request", Author: "owner"})
	// The queued record remains readable, but starting work cannot be persisted.
	blocked := filepath.Dir(q.path(&saved))
	if err := os.Chmod(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	q.Work(ctx, false, func(context.Context, string) (string, string, error) {
		t.Fatal("ran without durable claim")
		return "", "", nil
	}, nil)
	current, _ := q.Get(saved.ID)
	if current.Status != Queued || !current.Started.IsZero() || !q.served["owner"].IsZero() {
		t.Fatalf("failed claim was not restored: %+v", current)
	}
	if err := os.Chmod(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	q, _ = Open(dir)
	current, _ = q.Get(saved.ID)
	if current.Status != Queued {
		t.Fatal("failed claim changed durable record")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	count := 0
	q.Finished = func(Request) { cancel() }
	q.Work(ctx, false, func(context.Context, string) (string, string, error) { count++; return "done", "", nil }, nil)
	if count != 1 {
		t.Fatal("recovered claim did not execute exactly once")
	}
}
