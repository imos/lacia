package migrate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/doctor"
	"github.com/imos/lacia/internal/task"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func put(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const t1Dir = "work/tasks/2026-11/T1001-slug"

// fixture is a small repository of format 0 with every case the
// migration must handle.
func fixture(t *testing.T) (root, moves string) {
	t.Helper()
	root = t.TempDir()
	run(t, root, "init", "-q")
	put(t, root, ".lacia/config.json", "{\n  \"areas\": [\"work\", \"personal\"],\n  \"user\": \"田中さん\"\n}\n")
	put(t, root, ".gitignore", "/local/\n/*/tasks/*/*/files/\n")
	put(t, root, "AGENTS.md", "# 決まり\n\n- [タスク](work/tasks/2026-11/T1001-slug/task.md)\n- [消す決まり](operations/old.md) と [残す決まり](operations/keep.md#節)\n- 読むもの: AGENTS.md、work/tasks/2026-11/T1002/task.md、work/journal/2026/11/x.md\n- 領域の名前だけ: work と work/README.md\n- 相対の場所: ../work/tasks は触りません。personal/calendar.md は消します。\n- [外](https://example.com/work/tasks) [根](/work/tasks) [外へ](../outside.md)\n")
	put(t, root, "operations/old.md", "# 消す決まり\n")
	put(t, root, "operations/keep.md", "# 残す決まり\n\n[決まり](../AGENTS.md)\n")
	put(t, root, "personal/calendar.md", "控え\n")
	put(t, root, "migration/id-map.tsv", "work-20261101-01\tT1001\n")
	put(t, root, "work/journal/2026/11/x.md", "[タスク](../../../tasks/2026-11/T1001-slug/task.md)\n")
	put(t, root, "work/projects/old/x.md", "以前の資料。[タスク](../../tasks/2026-11/T1002/task.md)\n")
	put(t, root, t1Dir+"/task.md", "---\nid: T1001\ntitle: 名前のあるタスク\nstatus: review\ncreated: 2026-11-01\n---\n\n## いまの状況\n\n[規則](../../../../AGENTS.md)、[隣](../T1002/task.md)、[以前の資料](../../../projects/old/x.md)、[成果](artifacts/a.md)、[大きい](files/big.bin)、[ない](../../../../nowhere.md)。\n\n## 次にすること\n\n田中さんに見ていただきます。\n\n## 見ていただくもの\n\n- [成果](artifacts/a.md)\n\n```\n## 見ていただくもの\n[囲み](../../../../AGENTS.md)\n```\n")
	put(t, root, t1Dir+"/artifacts/a.md", "# 成果\n\n[規則](../../../../../AGENTS.md) と work/tasks/2026-11/T1002/task.md\n")
	put(t, root, t1Dir+"/log.md", strings.Join([]string{
		"# T1001 の履歴",
		"",
		"- 2026-11-01 10:00 タスクを作りました。状態は「未着手」です。（local-claude）",
		"- 2026-11-01 10:01 状態を「未着手」から「進行中」に変えました。担当は local-claude です。（local-claude）",
		"- 2026-11-01 10:02 files/big.bin を置きました（4B、sha256 0123456789abcdef、Gitには入りません）。（local-claude）",
		"- 2026-11-01 10:03 依頼を受け付けました（Web、web-1）。",
		"- 2026-11-01 10:04 due を「2026-12-01」にしました。（Web）",
		"- 2026-11-01 10:05 [規則](../../../../AGENTS.md)を読みました。（work-codex 2）（local-claude）",
		"- 2026-11-01 10:06 担当を local-claude から work-codex に替えました。（work-codex）",
		"- 2026-11-01 10:07 依頼を受け付けました（Web（画面）、web-2）。",
		"",
		"## これまでの経緯",
		"",
		"- 2026-10-30 始めました。",
		"  続きの行です。",
		"- 2026-10-31 進めました。",
		"",
		"### 2026-11-02",
		"",
		"まとめの段落です。",
		"- 2026-11-03 09:00 状態を「進行中」から「確認待ち」に変えました。（Web）",
		"",
	}, "\n"))
	put(t, root, t1Dir+"/files/big.bin", "big\n")
	put(t, root, "work/tasks/2026-11/T1002/task.md", "---\nid: T1002\ntitle: 名前のないタスク\nstatus: todo\ncreated: 2026-11-02\n---\n\n## いまの状況\n\n始めていません。\n\n## 次にすること\n\nレイシアが始めます。\n")
	put(t, root, "work/tasks/2026-11/T1002/log.md", "# T1002 の履歴\n\n前置きの行です。\n\n- 2026-11-02 08:00 タスクを作りました。状態は「未着手」です。\n")
	put(t, root, "work/projects/alpha/project.md", "---\ntitle: アルファ\n---\n\n## 目的\n\nめざします。\n\n## 方針\n\n- 小さく進めます。\n\n## 順序\n\n1. T1002\n")
	put(t, root, "work/projects/alpha/log.md", "# アルファの履歴\n\n- 2026-11-05 12:00 T1002 を、順序の1つ前へ動かしました。（Web）\n")
	put(t, root, "personal/knowledge/calendar-display-overrides.json", "{}\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "init")
	moves = filepath.Join(t.TempDir(), "moves.tsv")
	os.WriteFile(moves, []byte("# 追加の移動\n\nwork/journal\tarchive/journal/work\nwork/projects/old\tarchive/projects/work/old\noperations/old.md\t-\noperations/keep.md\trules/keep.md\npersonal/calendar.md\t-\n"), 0o644)
	return root, moves
}

func tracked(t *testing.T, root string) []string {
	run(t, root, "add", "-A")
	list := strings.Fields(run(t, root, "ls-files"))
	sort.Strings(list)
	return list
}

func TestMigrate(t *testing.T) {
	root, moves := fixture(t)
	before := tracked(t, root)
	run(t, root, "reset", "-q")
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	dry, err := Make(Options{Root: root, Moves: moves, DryRun: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "AGENTS.md"); !strings.Contains(got, "(work/tasks/2026-11/T1001-slug/task.md)") {
		t.Fatal("the dry run wrote something")
	}
	plan, err := Make(Options{Root: root, Moves: moves, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stats != dry.Stats {
		t.Errorf("dry run %+v, run %+v", dry.Stats, plan.Stats)
	}
	if err := plan.Run(now); err != nil {
		t.Fatal(err)
	}
	after := tracked(t, root)
	want := []string{
		".gitignore", ".lacia/calendar-overrides.json", ".lacia/config.json", ".lacia/id-map.tsv", "AGENTS.md", "TODO.md",
		"archive/journal/work/2026/11/x.md", "archive/projects/work/old/x.md",
		"areas/work/projects/alpha/log.jsonl", "areas/work/projects/alpha/project.md",
		"areas/work/tasks/2026-11/T1001-slug/artifacts/a.md", "areas/work/tasks/2026-11/T1001-slug/log.jsonl", "areas/work/tasks/2026-11/T1001-slug/task.md",
		"areas/work/tasks/2026-11/T1002/log.jsonl", "areas/work/tasks/2026-11/T1002/task.md",
		"rules/keep.md",
	}
	if strings.Join(after, "\n") != strings.Join(want, "\n") {
		t.Errorf("files after =\n%s", strings.Join(after, "\n"))
	}
	if len(before) != 17 {
		t.Errorf("files before = %d", len(before))
	}
	for _, gone := range []string{"operations", "migration", "work", "personal"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s/ is left", gone)
		}
	}
	if got := read(t, root, "areas/work/tasks/2026-11/T1001-slug/files/big.bin"); got != "big\n" {
		t.Errorf("files/ did not move: %q", got)
	}

	agents := read(t, root, "AGENTS.md")
	for _, want := range []string{
		"[タスク](areas/work/tasks/2026-11/T1001-slug/task.md)",
		"[消す決まり](operations/old.md)", // a removed file keeps its link
		"[残す決まり](rules/keep.md#節)",
		"読むもの: AGENTS.md、areas/work/tasks/2026-11/T1002/task.md、archive/journal/work/2026/11/x.md",
		"領域の名前だけ: work と work/README.md",
		"../work/tasks は触りません。personal/calendar.md は消します。",
		"[外](https://example.com/work/tasks) [根](/work/tasks) [外へ](../outside.md)",
	} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md lacks %q:\n%s", want, agents)
		}
	}
	taskMD := read(t, root, "areas/work/tasks/2026-11/T1001-slug/task.md")
	for _, want := range []string{
		"[規則](../../../../../AGENTS.md)、[隣](../T1002/task.md)、[以前の資料](../../../../../archive/projects/work/old/x.md)、[成果](artifacts/a.md)、[大きい](files/big.bin)、[ない](../../../../../nowhere.md)。",
		"## いまの資料\n\n- [成果](artifacts/a.md)",
		"```\n## 見ていただくもの\n[囲み](../../../../AGENTS.md)\n```",
	} {
		if !strings.Contains(taskMD, want) {
			t.Errorf("task.md lacks %q:\n%s", want, taskMD)
		}
	}
	// Artifacts keep their words; only links follow.
	if got := read(t, root, "areas/work/tasks/2026-11/T1001-slug/artifacts/a.md"); got != "# 成果\n\n[規則](../../../../../../AGENTS.md) と work/tasks/2026-11/T1002/task.md\n" {
		t.Errorf("artifact = %q", got)
	}
	if got := read(t, root, "rules/keep.md"); got != "# 残す決まり\n\n[決まり](../AGENTS.md)\n" {
		t.Errorf("rules/keep.md = %q", got)
	}
	if got := read(t, root, "archive/journal/work/2026/11/x.md"); got != "[タスク](../../../tasks/2026-11/T1001-slug/task.md)\n" {
		t.Errorf("archive was rewritten: %q", got)
	}
	project := read(t, root, "areas/work/projects/alpha/project.md")
	if !strings.Contains(project, "## めざすこと\n") || !strings.Contains(project, "## 進め方\n") || !strings.Contains(project, "## 取りかかる順\n\n1. T1002") {
		t.Errorf("project.md:\n%s", project)
	}
	if got := read(t, root, ".gitignore"); got != "/local/\n/areas/*/tasks/*/*/files/\n/.lacia/source/\n" {
		t.Errorf(".gitignore = %q", got)
	}
	cfg, err := task.LoadConfig(root)
	if err != nil || cfg.Format != 1 || cfg.Timezone != "Asia/Tokyo" || cfg.User != "田中さん" {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	if !strings.HasPrefix(read(t, root, ".lacia/config.json"), "{\n  \"format\": 1,\n  \"areas\"") {
		t.Errorf("config text:\n%s", read(t, root, ".lacia/config.json"))
	}
	if !strings.Contains(read(t, root, "TODO.md"), "(areas/work/tasks/") {
		t.Errorf("TODO.md:\n%s", read(t, root, "TODO.md"))
	}

	// The history, event by event.
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimRight(read(t, root, "areas/work/tasks/2026-11/T1001-slug/log.jsonl"), "\n"), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("%v: %s", err, line)
		}
		events = append(events, e)
	}
	wantEvents := []string{
		`{"at":"2026-11-01T10:00:00+09:00","by":"local-claude","imported":true,"status":"todo","text":"タスクを作りました。状態は「未着手」です。（local-claude）","type":"created"}`,
		`{"at":"2026-11-01T10:01:00+09:00","by":"local-claude","from":"todo","imported":true,"owner":"local-claude","text":"状態を「未着手」から「進行中」に変えました。担当は local-claude です。（local-claude）","to":"doing","type":"status"}`,
		`{"action":"put","at":"2026-11-01T10:02:00+09:00","by":"local-claude","imported":true,"path":"files/big.bin","sha256":"0123456789abcdef","text":"files/big.bin を置きました（4B、sha256 0123456789abcdef、Gitには入りません）。（local-claude）","type":"file"}`,
		`{"at":"2026-11-01T10:03:00+09:00","imported":true,"request":"web-1","text":"依頼を受け付けました（Web、web-1）。","type":"request","via":"Web"}`,
		`{"at":"2026-11-01T10:04:00+09:00","by":"Web","imported":true,"key":"due","text":"due を「2026-12-01」にしました。（Web）","type":"field","value":"2026-12-01"}`,
		`{"at":"2026-11-01T10:05:00+09:00","by":"local-claude","imported":true,"text":"[規則](../../../../../AGENTS.md)を読みました。（work-codex 2）（local-claude）","type":"note"}`,
		`{"at":"2026-11-01T10:06:00+09:00","by":"work-codex","imported":true,"owner":"work-codex","previous":"local-claude","text":"担当を local-claude から work-codex に替えました。（work-codex）","type":"claimed"}`,
		`{"at":"2026-11-01T10:07:00+09:00","imported":true,"text":"依頼を受け付けました（Web（画面）、web-2）。","type":"note"}`,
		`{"at":"2026-11-01T10:07:00+09:00","imported":true,"text":"## これまでの経緯","type":"legacy"}`,
		`{"at":"2026-10-30T00:00:00+09:00","imported":true,"text":"- 2026-10-30 始めました。\n  続きの行です。","type":"legacy"}`,
		`{"at":"2026-10-31T00:00:00+09:00","imported":true,"text":"- 2026-10-31 進めました。","type":"legacy"}`,
		`{"at":"2026-11-02T00:00:00+09:00","imported":true,"text":"### 2026-11-02\n\nまとめの段落です。","type":"legacy"}`,
		`{"at":"2026-11-03T09:00:00+09:00","by":"Web","from":"doing","imported":true,"text":"状態を「進行中」から「確認待ち」に変えました。（Web）","to":"review","type":"status"}`,
	}
	if len(events) != len(wantEvents) {
		t.Fatalf("got %d events:\n%s", len(events), read(t, root, "areas/work/tasks/2026-11/T1001-slug/log.jsonl"))
	}
	for i, e := range events {
		got, _ := json.Marshal(e)
		if string(got) != wantEvents[i] {
			t.Errorf("event %d =\n%s\nwant\n%s", i, got, wantEvents[i])
		}
	}
	if got := read(t, root, "areas/work/tasks/2026-11/T1002/log.jsonl"); !strings.HasPrefix(got, `{"at":"2026-11-02T00:00:00+09:00","type":"legacy","text":"前置きの行です。","imported":true}`+"\n") {
		t.Errorf("T1002 history = %s", got)
	}
	if got := read(t, root, "areas/work/projects/alpha/log.jsonl"); got != `{"at":"2026-11-05T12:00:00+09:00","type":"note","by":"Web","text":"T1002 を、順序の1つ前へ動かしました。（Web）","imported":true}`+"\n" {
		t.Errorf("project history = %s", got)
	}
	if plan.Stats.Histories != 3 || plan.Stats.Events != 11 || plan.Stats.Blocks != 5 || plan.Stats.Headings != 4 {
		t.Errorf("stats = %+v", plan.Stats)
	}
	attention := strings.Join(plan.Attention, "\n")
	for _, want := range []string{"AGENTS.md:4 消すファイルを指しています: operations/old.md", "消すファイルを指しています: personal/calendar.md", "リンクの先が、移す前からありません: ../../../../nowhere.md", "AGENTS.md:6 areas/ の付いていない場所が残っています: work/README.md"} {
		if !strings.Contains(attention, want) {
			t.Errorf("attention lacks %q:\n%s", want, attention)
		}
	}
	store, err := task.Open(root)
	if err != nil || len(store.List()) != 2 {
		t.Fatalf("store after: %v", err)
	}
	if got, err := store.Get("work-20261101-01"); err != nil || got.ID != "T1001" {
		t.Errorf("alias after: %+v %v", got, err)
	}
	if r := doctor.Check(root, false); len(r.Errors) != 0 {
		t.Errorf("doctor: %v", r.Errors)
	}

	// Committed or not, --undo puts everything back, files/ included.
	run(t, root, "commit", "-q", "-m", "migrate")
	if _, err := Undo(root); err != nil {
		t.Fatal(err)
	}
	if got := tracked(t, root); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("files after undo =\n%s", strings.Join(got, "\n"))
	}
	if diff := run(t, root, "diff", "--cached", "--stat", "HEAD~1"); diff != "" {
		t.Errorf("undo left differences:\n%s", diff)
	}
	if got := read(t, root, t1Dir+"/files/big.bin"); got != "big\n" {
		t.Errorf("files/ did not come back: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "areas")); !os.IsNotExist(err) {
		t.Error("areas/ is left after undo")
	}
}

func TestMigrateStopsFirst(t *testing.T) {
	root, moves := fixture(t)
	put(t, root, "dirty.md", "まだコミットしていません\n")
	if _, err := Make(Options{Root: root, Moves: moves}); err == nil || !strings.Contains(err.Error(), "コミットしていない") {
		t.Errorf("dirty: %v", err)
	}
	os.Remove(filepath.Join(root, "dirty.md"))
	put(t, root, ".git/lacia-server", "http://127.0.0.1:1\n")
	if _, err := Make(Options{Root: root, Moves: moves}); err == nil || !strings.Contains(err.Error(), "サーバー") {
		t.Errorf("served: %v", err)
	}
	os.Remove(filepath.Join(root, ".git/lacia-server"))
	if _, err := Make(Options{Root: root, Moves: moves, ReadOnly: true}); err == nil {
		t.Error("a read-only copy was migrated")
	}
	bad := filepath.Join(t.TempDir(), "bad.tsv")
	os.WriteFile(bad, []byte("operations/keep.md\twork/rules.md\n"), 0o644)
	if _, err := Make(Options{Root: root, Moves: bad}); err == nil || !strings.Contains(err.Error(), "領域の中") {
		t.Errorf("into an area: %v", err)
	}
	os.WriteFile(bad, []byte("nothing/here.md\t-\n"), 0o644)
	if _, err := Make(Options{Root: root, Moves: bad}); err == nil {
		t.Error("a missing path was accepted")
	}
	// A history that would not come out the same stops everything.
	if _, _, _, err := Convert("- 2026-11-01 10:00 \xff\n", true, "2026-11-01"); err == nil {
		t.Error("broken text was converted")
	}
	if status := run(t, root, "status", "--short"); status != "" {
		t.Errorf("checks wrote something:\n%s", status)
	}
	put(t, root, ".lacia/config.json", "{\"format\": 2}\n")
	run(t, root, "commit", "-q", "-am", "format 2")
	if _, err := Make(Options{Root: root}); err == nil || !strings.Contains(err.Error(), "すでに形式2") {
		t.Errorf("format 2: %v", err)
	}
}
