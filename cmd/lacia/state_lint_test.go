package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/doctor"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/web"
)

func TestWaitingGuidanceInStandaloneAndConnectedCLI(t *testing.T) {
	for _, format := range []int{1, 2} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			t.Setenv("LACIA_API", "")
			root := t.TempDir()
			taskMirrorFixture(t, root, false)
			if err := os.WriteFile(filepath.Join(root, task.ConfigFile), fmt.Appendf(nil, `{"format":%d}`, format), 0o600); err != nil {
				t.Fatal(err)
			}
			const id = "T1001"
			out, advice, err := captureMirrorTask(t, "set", "--root", root, id, "waiting")
			if err != nil || !strings.Contains(out, "にしました") || !strings.Contains(advice, task.WaitingConditionWarning) || !strings.Contains(advice, "task set T1001 review") {
				t.Fatalf("standalone set: %q %q %v", out, advice, err)
			}
			out, _, err = captureMirrorTask(t, "lint", "--root", root, id)
			if err != nil || !strings.Contains(out, task.WaitingConditionWarning) {
				t.Fatalf("standalone lint: %q %v", out, err)
			}
			state := t.TempDir()
			if err := os.WriteFile(filepath.Join(state, "sessions.json"), []byte("[]"), 0o600); err != nil {
				t.Fatal(err)
			}
			handovers, err := handover.Open(filepath.Join(state, "handovers"))
			if err != nil {
				t.Fatal(err)
			}
			pending, _, err := handovers.Add(handover.Handover{TaskID: id, Next: "local-codex", Then: "結果を確かめます。"})
			if err != nil {
				t.Fatal(err)
			}
			out, _, err = captureMirrorTask(t, "lint", "--root", root, "--state", state, id)
			if err != nil || strings.Contains(out, task.WaitingConditionWarning) {
				t.Fatalf("standalone lint with a handover: %q %v", out, err)
			}
			store, err := task.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := (&web.Server{Store: store, Handovers: handovers}).Handler()
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			t.Setenv("LACIA_API", server.URL)
			out, _, err = captureMirrorTask(t, "lint", "--root", root, id)
			if err != nil || strings.Contains(out, task.WaitingConditionWarning) {
				t.Fatalf("connected lint with a handover: %q %v", out, err)
			}
			if _, _, err := handovers.Update(pending.ID, func(h *handover.Handover) bool { h.State = handover.Acked; return true }); err != nil {
				t.Fatal(err)
			}
			out, _, err = captureMirrorTask(t, "lint", "--root", root, id)
			if err != nil || !strings.Contains(out, task.WaitingConditionWarning) {
				t.Fatalf("connected lint after acknowledgement: %q %v", out, err)
			}
			_, advice, err = captureMirrorTask(t, "set", "--root", root, id, "waiting")
			if err != nil || !strings.Contains(advice, task.WaitingConditionWarning) {
				t.Fatalf("connected set: %q %v", advice, err)
			}
		})
	}
}

func TestStateChecksAreSharedAndReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "areas", "personal"), 0755); err != nil {
		t.Fatal(err)
	}
	dir := taskMirrorFixture(t, root, true)
	body := "---\nid: T1001\ntitle: 会話を確かめる\nstatus: doing\ncreated: 2026-11-25\nowner: local-codex\nowner_session: known\n---\n\n## ひとことで\n\n会話を確かめます。\n\n## 次にすること\n\nレイシアが進めます。\n"
	if err := os.WriteFile(filepath.Join(dir, task.TaskFile), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	sessions, _ := json.Marshal([]caller.Session{{Key: "known", Name: "local-codex", Last: time.Now().Add(-24 * time.Hour)}})
	sessionPath := filepath.Join(state, "sessions.json")
	if err := os.WriteFile(sessionPath, sessions, 0600); err != nil {
		t.Fatal(err)
	}
	// Neither command may query an API, even for a read-only copy.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API request: %s", r.URL)
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	t.Setenv("LACIA_API", server.URL)
	t.Setenv("LACIA_TASKS", "")
	out, _, err := captureMirrorTask(t, "lint", "--root", root, "--state", state, "T1001")
	if err != nil || !strings.Contains(out, "12時間以上前") {
		t.Fatalf("lint: %q %v", out, err)
	}
	context, err := stateLintContext(state)
	if err != nil {
		t.Fatal(err)
	}
	report := doctor.CheckWithState(root, false, context)
	notes := strings.Join(report.Notes, "\n")
	if len(report.Errors) != 0 || !strings.Contains(notes, "12時間以上前") {
		t.Fatalf("doctor: %+v", report)
	}
	plain := doctor.Check(root, false)
	if strings.Contains(strings.Join(plain.Notes, "\n"), "12時間以上前") {
		t.Fatal("inferred activity without state")
	}
	out, _, err = captureMirrorTask(t, "lint", "--root", root, "T1001")
	if err != nil || strings.Contains(out, "12時間以上前") {
		t.Fatalf("lint without state: %q %v", out, err)
	}
	for path, want := range map[string][]byte{filepath.Join(dir, task.TaskFile): []byte(body), sessionPath: sessions} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("changed %s: %v", path, err)
		}
	}
}

func TestStateLintRejectsUnreadableExplicitState(t *testing.T) {
	state := t.TempDir()
	if _, err := stateLintContext(state); err == nil {
		t.Fatal("missing sessions accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "sessions.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := stateLintContext(state); err == nil {
		t.Fatal("malformed sessions accepted")
	}
}

func TestStateConditionsInDoctorAndSelectedLint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "areas", "personal"), 0755); err != nil {
		t.Fatal(err)
	}
	dir := taskMirrorFixture(t, root, false)
	body := "---\nid: T1001\ntitle: 確認する\nstatus: review\ncreated: 2026-11-25\nresume_on: 2999-01-01\nafter: T1002\n---\n\n## ひとことで\n\n確認します。\n\n## 次にすること\n\n利用者さんに確認していただきます。\n"
	if err := os.WriteFile(filepath.Join(dir, task.TaskFile), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(dir), "T1002")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	dependency := strings.ReplaceAll(strings.ReplaceAll(body, "T1001", "T1002"), "status: review\n", "status: todo\n")
	dependency = strings.ReplaceAll(dependency, "resume_on: 2999-01-01\nafter: T1002\n", "")
	if err := os.WriteFile(filepath.Join(other, task.TaskFile), []byte(dependency), 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := captureMirrorTask(t, "lint", "--root", root, "T1001")
	if err != nil {
		t.Fatal(err)
	}
	report := doctor.Check(root, false)
	if len(report.Errors) != 0 {
		t.Fatal(report.Errors)
	}
	for _, want := range []string{"確認待ちですが", "after で待つタスク T1002"} {
		if !strings.Contains(out, want) || !strings.Contains(strings.Join(report.Notes, "\n"), want) {
			t.Fatalf("missing %q: lint %s doctor %v", want, out, report)
		}
	}
}
