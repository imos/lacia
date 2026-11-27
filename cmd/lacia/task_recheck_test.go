package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/web"
)

// The commands register a recheck and record its results, with and without
// a server.
func TestRecheckCommands(t *testing.T) {
	for _, connected := range []bool{false, true} {
		name := "standalone"
		if connected {
			name = "connected"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("LACIA_API", "")
			root := t.TempDir()
			dir := taskMirrorFixture(t, root, false)
			const id = "T1001"
			if connected {
				store, err := task.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				handler, err := (&web.Server{Store: store}).Handler()
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(handler)
				defer server.Close()
				t.Setenv("LACIA_API", server.URL)
			}
			stored := func() string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(dir, task.TaskFile))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			if _, _, err := captureMirrorTask(t, "recheck", "--root", root, id, "--every", "1d"); err == nil || !strings.Contains(err.Error(), "--until") {
				t.Fatalf("registered with no deadline: %v", err)
			}
			if _, _, err := captureMirrorTask(t, "rechecked", "--root", root, id, "same", "変化はありませんでした。"); err == nil || !strings.Contains(err.Error(), "登録がありません") {
				t.Fatalf("a result before the registration: %v", err)
			}
			out, _, err := captureMirrorTask(t, "recheck", "--root", root, id, "--every", "1d", "--until", "2999-01-01")
			if err != nil || !strings.Contains(out, "T1001 を外部待ちにして、毎日") || !strings.Contains(out, "に確認（2999-01-01 まで）と登録しました") || !strings.Contains(out, "「次にすること」に") {
				t.Fatalf("register: %q %v", out, err)
			}
			if body := stored(); !strings.Contains(body, "status: waiting") || !strings.Contains(body, "recheck_every: 1d") || !strings.Contains(body, "recheck_until: 2999-01-01") || !strings.Contains(body, "resume_on: ") {
				t.Fatalf("task.md after registering:\n%s", body)
			}
			if out, _, err := captureMirrorTask(t, "recheck", "--root", root, "--every", "1d", "--until", "2999-01-01", "--next", nextRound(stored()), id); err != nil || !strings.Contains(out, "同じ確認がすでに登録されています") {
				t.Fatalf("the same registration again: %q %v", out, err)
			}
			out, _, err = captureMirrorTask(t, "lint", "--root", root, id)
			if err != nil || strings.Contains(out, task.WaitingConditionWarning) || strings.Contains(out, "定義されていない項目") {
				t.Fatalf("lint of a task with rounds: %q %v", out, err)
			}
			out, _, err = captureMirrorTask(t, "rechecked", "--root", root, id, "unknown", "認証が切れていて、読めませんでした。")
			if err != nil || !strings.Contains(out, "「確かめられなかった」と記録しました") || !strings.Contains(stored(), "recheck_missed: 1") {
				t.Fatalf("unknown: %q %v\n%s", out, err, stored())
			}
			out, _, err = captureMirrorTask(t, "lint", "--root", root, id)
			if err != nil || !strings.Contains(out, "確かめられませんでした") {
				t.Fatalf("lint after a look that failed: %q %v", out, err)
			}
			out, _, err = captureMirrorTask(t, "rechecked", "--root", root, id, "same", "新しいコメントは", "ありませんでした。")
			if err != nil || !strings.Contains(out, "「変化なし」と記録しました") || strings.Contains(stored(), "recheck_missed") || !strings.Contains(stored(), "recheck_last: ") {
				t.Fatalf("same: %q %v\n%s", out, err, stored())
			}
			out, _, err = captureMirrorTask(t, "rechecked", "--root", root, id, "changed", "修正の依頼が届きました。")
			if err != nil || !strings.Contains(out, "確認待ちにしました") {
				t.Fatalf("changed: %q %v", out, err)
			}
			if body := stored(); !strings.Contains(body, "status: review") || strings.Contains(body, "recheck_") || strings.Contains(body, "resume_on") {
				t.Fatalf("task.md after a change:\n%s", body)
			}
		})
	}
}

// nextRound reads resume_on from a task.md.
func nextRound(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(line, "resume_on: "); ok {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}
