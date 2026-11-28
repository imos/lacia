package task

import (
	"github.com/imos/lacia/internal/testrecords"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDocRoundTrip(t *testing.T) {
	src := "---\nid: T1001\ntitle: \"設定: 標準の場所へ移す\"\nstatus: review\ncreated: 2026-11-12\n---\n\n## いまの状況\n\n終わりました。\n"
	d, err := ParseDoc(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Get("title"); got != "設定: 標準の場所へ移す" {
		t.Errorf("title = %q", got)
	}
	if got := d.String(); got != src {
		t.Errorf("round trip changed the file:\n%s", got)
	}
	d.Set("status", "done")
	d.Set("done", "2026-11-13")
	if !strings.Contains(d.String(), "status: done\ncreated: 2026-11-12\ndone: 2026-11-13\n---") {
		t.Errorf("fields out of order:\n%s", d.String())
	}
}

func TestParseDocRejects(t *testing.T) {
	for name, src := range map[string]string{
		"no front matter": "# title\n",
		"unclosed":        "---\nid: T1\n",
		"nested":          "---\nid: T1\ntags: [a, b]\n---\n",
		"duplicate":       "---\nid: T1\nid: T2\n---\n",
		"not a pair":      "---\njust text\n---\n",
	} {
		if _, err := ParseDoc(src); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSection(t *testing.T) {
	body := "## いまの状況\n\nA\n\n## 次にすること\n\nB\nC\n\n## 見ていただくもの\n\n- D\n"
	if got := Section(body, "次にすること"); got != "B\nC" {
		t.Errorf("Section = %q", got)
	}
	if got := Section(body, "ない見出し"); got != "" {
		t.Errorf("missing section = %q", got)
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(testrecords.Init(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 12, 1, 30, 0, 0, time.UTC) } // 10:30 JST
	return s
}

func TestCreateAllocatesSequentialIDs(t *testing.T) {
	s := newStore(t)
	a, err := s.Create(NewTask{Area: "work", Title: "最初のタスク", Slug: "first", Actor: "テスト"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "T1001" || a.Dir != "areas/work/tasks/2026-11/T1001-first" || a.Created != "2026-11-12" || a.Status != Todo {
		t.Errorf("first task = %+v", a)
	}
	b, err := s.Create(NewTask{Area: "personal", Title: "次のタスク", Status: Doing, Parent: "T1001"})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != "T1002" || b.Parent != "T1001" {
		t.Errorf("second task = %+v", b)
	}
	if _, err := s.Create(NewTask{Area: "family", Title: "x"}); err == nil {
		t.Error("unknown area was accepted")
	}
	if _, err := s.Create(NewTask{Area: "work", Title: "x", Parent: "T9999"}); err == nil {
		t.Error("missing parent was accepted")
	}

	// A fresh store must see the same tasks and continue the series.
	again, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	again.now = s.now
	c, err := again.Create(NewTask{Area: "work", Title: "三つ目"})
	if err != nil || c.ID != "T1003" {
		t.Errorf("after reload: %+v, %v", c, err)
	}
	if kids := again.Children("T1001"); len(kids) != 1 || kids[0].ID != "T1002" {
		t.Errorf("children = %+v", kids)
	}
}

func TestSetStatusKeepsOutsideEdits(t *testing.T) {
	s := newStore(t)
	a, _ := s.Create(NewTask{Area: "work", Title: "状態を変える"})
	path := filepath.Join(s.Root(), a.Dir, "task.md")
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), "（まだ書かれていません）", "Codexが書いた文章です。", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	got, changed, err := s.SetStatus("t1001", Review, "Web", "")
	if err != nil || !changed {
		t.Fatalf("SetStatus: %v changed=%v", err, changed)
	}
	if got.Status != Review || got.Done != "" || !strings.Contains(got.Body, "Codexが書いた文章です。") {
		t.Errorf("after review: %+v", got)
	}
	got, _, _ = s.SetStatus("T1001", Done, "Web", "")
	if got.Done != "2026-11-12" {
		t.Errorf("done date = %q", got.Done)
	}
	if _, changed, _ := s.SetStatus("T1001", Done, "Web", ""); changed {
		t.Error("setting the same status reported a change")
	}
	got, _, _ = s.SetStatus("T1001", Doing, "Web", "やり直します。")
	if got.Done != "" {
		t.Errorf("reopened task kept done date %q", got.Done)
	}
	if _, _, err := s.SetStatus("T1001", "finished", "Web", ""); err == nil {
		t.Error("invalid status was accepted")
	}

	log, _ := readTestHistory(t, filepath.Join(s.Root(), a.Dir))
	for _, want := range []string{
		"- 2026-11-12 10:30 タスクを作りました。状態は「未着手」です。",
		"状態を「未着手」から「確認待ち」に変えました。（Web）",
		"状態を「完了」から「進行中」に変えました。やり直します。（Web）",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log.md lacks %q:\n%s", want, log)
		}
	}
}

func TestReloadReportsProblems(t *testing.T) {
	s := newStore(t)
	s.Create(NewTask{Area: "work", Title: "正しいタスク"})
	bad := filepath.Join(s.Root(), "areas/work/tasks/2026-11/T1002")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "task.md"), []byte("---\nid: T1002\ntitle: 状態が変\nstatus: finished\ncreated: 2026-11-12\n---\n"), 0o644)
	os.MkdirAll(filepath.Join(s.Root(), ".lacia"), 0o755)
	os.WriteFile(filepath.Join(s.Root(), AliasFile), []byte("work-20261112-01\tT1001\n"), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if p := s.Problems(); len(p) != 1 || !strings.Contains(p[0].Err, "status") {
		t.Errorf("problems = %+v", p)
	}
	if got, err := s.Get("work-20261112-01"); err != nil || got.ID != "T1001" {
		t.Errorf("alias lookup: %+v, %v", got, err)
	}
}

func TestLint(t *testing.T) {
	bad := "## いまの状況\n\nPHP/CMS実動/公開反映/復旧手順を未実証として明示引継ぎ。\n実行環境・公開方法・復旧手順の検証。\nevent-0fdb910e72f958d0 を受領。\nreadbackして確かめました。\n"
	got := Lint(bad)
	text := ""
	for _, w := range got {
		text += w.String() + "\n"
	}
	for _, want := range []string{"スラッシュ", "中黒", "で終わる文", "IDやハッシュ", "英語をそのまま動詞"} {
		if !strings.Contains(text, want) {
			t.Errorf("no warning about %s in:\n%s", want, text)
		}
	}
	good := "## いまの状況\n\nローカル環境での移行は終わり、ブラウザーの接続も戻りました。収集は11/13(金)で、[openai/codexのIssue](https://example.com/a/b)とgo/assignを見ました。\n結果は [移行の結果](artifacts/result.md) にまとめました。`work/tasks` も確認しました。\n\n```\na/b/c・d・e の検証。\n```\n"
	if got := Lint(good); len(got) != 0 {
		t.Errorf("good text got warnings: %v", got)
	}
}

func TestCheck(t *testing.T) {
	good := Task{ID: "T1001", Status: Review, Body: "## いまの状況\n\n終わりました。\n\n## 次にすること\n\nオーナーさんに、結果を見ていただきます。\n\n## いまの資料\n\n- [結果](artifacts/a.md)\n"}
	if w := Check(good, []string{"task.md", "log.jsonl", "notes.md", "artifacts/a.md"}); len(w) != 0 {
		t.Errorf("a well-formed task got warnings: %v", w)
	}
	bad := Task{ID: "T1002", Status: Doing, Body: "# 題名\n\n## いまの状況\n\n（まだ書かれていません）\n\n## 2026-11-12 進捗\n\n調べました。\n\n```\n## コードの中\n```\n"}
	text := ""
	for _, w := range Check(bad, []string{"task.md", "memo.txt", "artifacts/x.png"}) {
		text += w.String() + "\n"
	}
	for _, want := range []string{"書式が違います", "「2026-11-12 進捗」という見出しは", "「## 次にすること」がありません", "まだ書かれていません）」が残っています", "memo.txt は"} {
		if !strings.Contains(text, want) {
			t.Errorf("no warning containing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "コードの中") || strings.Contains(text, "x.png") {
		t.Errorf("unexpected warning:\n%s", text)
	}
	waiting := Task{ID: "T1003", Status: Review, Body: "## いまの状況\n\nできました。\n\n## 次にすること\n\n確認を待ちます。\n"}
	if w := Check(waiting, nil); len(w) != 1 || !strings.Contains(w[0].Message, "確認する方の名前") {
		t.Errorf("review without an addressee: %v", w)
	}
	if w := Check(Task{ID: "T1004", Status: Done, Body: "## いまの状況\n\n完了しました。\n"}, nil); len(w) != 0 {
		t.Errorf("a closed task needs no next step: %v", w)
	}
}

func TestWake(t *testing.T) {
	s := newStore(t)
	due, _ := s.Create(NewTask{Area: "work", Title: "日付が来たタスク"})
	later, _ := s.Create(NewTask{Area: "work", Title: "まだ先のタスク"})
	closed, _ := s.Create(NewTask{Area: "work", Title: "閉じたタスク"})
	s.SetField(due.ID, "resume_on", "2026-11-19", "")
	s.SetField(later.ID, "resume_on", "2026-11-20", "")
	s.SetField(closed.ID, "resume_on", "2026-11-19", "")
	s.SetStatus(closed.ID, Done, "", "")

	day := func(d int, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, JST) }
	if woken := s.Wake(day(18, 23)); len(woken) != 0 {
		t.Fatalf("woken a day early: %v", woken)
	}
	woken := s.Wake(day(19, 0))
	if len(woken) != 1 || woken[0].ID != due.ID {
		t.Fatalf("woken = %v", woken)
	}
	got, _ := s.Get(due.ID)
	if got.Status != Review || got.ResumeOn != "" {
		t.Errorf("after waking: status %s, resume_on %q", got.Status, got.ResumeOn)
	}
	log, _ := readTestHistory(t, filepath.Join(s.Root(), got.Dir))
	if !strings.Contains(string(log), "指定の日時（2026-11-19）になりました。着手できるようになったので") {
		t.Errorf("log:\n%s", log)
	}
	if again := s.Wake(day(19, 1)); len(again) != 0 {
		t.Errorf("woken twice: %v", again)
	}
	if got, _ := s.Get(later.ID); got.Status != Todo || got.ResumeOn != "2026-11-20" {
		t.Errorf("a later task changed: %+v", got)
	}
	if got, _ := s.Get(closed.ID); got.Status != Done {
		t.Errorf("a closed task changed: %+v", got)
	}
}

func TestWakeAtATime(t *testing.T) {
	s := newStore(t)
	hidden, _ := s.Create(NewTask{Area: "work", Title: "15時まで待機"})
	listed, _ := s.Create(NewTask{Area: "work", Title: "着手待ちのまま、15時に要対応へ"})
	if _, _, err := s.SetField(hidden.ID, "resume_on", "2026-11-19T15:00", ""); err != nil {
		t.Fatal(err)
	}
	s.SetField(listed.ID, "review_at", "2026-11-19 15:00", "")
	if _, _, err := s.SetField(listed.ID, "review_at", "あした", ""); err == nil {
		t.Error("a value that is not a time was accepted")
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 19, h, m, 0, 0, JST) }
	h, _ := s.Get(hidden.ID)
	l, _ := s.Get(listed.ID)
	if h.ResumeOn != "2026-11-19 15:00" || !h.Snoozed(at(14, 59)) || h.Snoozed(at(15, 0)) {
		t.Errorf("hidden: %+v", h)
	}
	if l.ReviewAt != "2026-11-19 15:00" || l.Snoozed(at(9, 0)) {
		t.Errorf("listed: %+v", l)
	}
	if woken := s.Wake(at(14, 59)); len(woken) != 0 {
		t.Fatalf("woken early: %v", woken)
	}
	if woken := s.Wake(at(15, 0)); len(woken) != 2 {
		t.Fatalf("woken = %v", woken)
	}
	for _, id := range []string{hidden.ID, listed.ID} {
		if got, _ := s.Get(id); got.Status != Review || got.ResumeOn != "" || got.ReviewAt != "" {
			t.Errorf("%s after waking: %+v", id, got)
		}
	}
}

func TestWakeToStart(t *testing.T) {
	s := newStore(t)
	auto, _ := s.Create(NewTask{Area: "work", Title: "15時にレイシアが着手するタスク"})
	s.SetField(auto.ID, "review_at", "2026-11-19 15:00", "")
	if _, _, err := s.SetField(auto.ID, "on_ready", "review", ""); err == nil {
		t.Error("an unknown on_ready was accepted")
	}
	if _, _, err := s.SetField(auto.ID, "on_ready", "start", ""); err != nil {
		t.Fatal(err)
	}
	woken := s.Wake(time.Date(2026, 11, 19, 15, 0, 0, 0, JST))
	if len(woken) != 1 || woken[0].OnReady != OnReadyStart {
		t.Fatalf("woken = %+v", woken)
	}
	got, _ := s.Get(auto.ID)
	if got.Status != Todo || got.ReviewAt != "" || got.OnReady != "" || got.ReadySince != "" {
		t.Errorf("after waking: %+v", got)
	}
	log, _ := readTestHistory(t, filepath.Join(s.Root(), got.Dir))
	if !strings.Contains(string(log), "指定の日時（2026-11-19 15:00）になりました。自動で着手する設定なので、会話に作業の開始を依頼します。") {
		t.Errorf("log:\n%s", log)
	}
}

func TestWakeAfterOtherTasks(t *testing.T) {
	s := newStore(t)
	a, _ := s.Create(NewTask{Area: "work", Title: "先に終えるタスク"})
	b, _ := s.Create(NewTask{Area: "work", Title: "もう1つの前提"})
	c, _ := s.Create(NewTask{Area: "work", Title: "2つを待つタスク"})
	d, _ := s.Create(NewTask{Area: "work", Title: "前提と日時の両方を待つタスク"})
	if _, _, err := s.SetField(c.ID, "after", a.ID+"、 "+b.ID, ""); err != nil {
		t.Fatal(err)
	}
	s.SetField(d.ID, "after", a.ID, "")
	s.SetField(d.ID, "resume_on", "2026-11-19", "")
	for _, bad := range []string{"Z9999", c.ID, "あとで"} {
		if _, _, err := s.SetField(c.ID, "after", bad, ""); err == nil {
			t.Errorf("after %q was accepted", bad)
		}
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, JST)
	got, _ := s.Get(c.ID)
	if strings.Join(got.After, ",") != a.ID+","+b.ID || len(got.Blockers) != 2 || !got.Snoozed(now) {
		t.Fatalf("waiting task: %+v", got)
	}
	s.SetStatus(a.ID, Done, "", "")
	if woken := s.Wake(now); len(woken) != 0 {
		t.Fatalf("woken with one of two open, or before its date: %v", woken)
	}
	if got, _ := s.Get(c.ID); strings.Join(got.Blockers, ",") != b.ID {
		t.Errorf("blockers = %v", got.Blockers)
	}
	s.SetStatus(b.ID, Cancelled, "", "")
	woken := s.Wake(now)
	if len(woken) != 1 || woken[0].ID != c.ID {
		t.Fatalf("woken = %v", woken)
	}
	got, _ = s.Get(c.ID)
	if got.Status != Review || len(got.After) != 0 || got.ReadySince == "" || got.Snoozed(now) {
		t.Errorf("after waking: %+v", got)
	}
	log, _ := readTestHistory(t, filepath.Join(s.Root(), got.Dir))
	if !strings.Contains(string(log), a.ID+" が完了しました。"+b.ID+" は取りやめになりました。着手できるようになったので") {
		t.Errorf("log:\n%s", log)
	}
	// Starting the task clears the mark.
	s.SetStatus(c.ID, Doing, "", "")
	if got, _ := s.Get(c.ID); got.ReadySince != "" {
		t.Errorf("ready_since kept: %+v", got)
	}
	// The other one needs its date as well.
	if woken := s.Wake(time.Date(2026, 11, 19, 0, 1, 0, 0, JST)); len(woken) != 1 || woken[0].ID != d.ID {
		t.Errorf("woken on the date = %v", woken)
	}
}

func TestDependencyCircle(t *testing.T) {
	s := newStore(t)
	a, _ := s.Create(NewTask{Area: "work", Title: "A"})
	b, _ := s.Create(NewTask{Area: "work", Title: "B"})
	s.SetField(a.ID, "after", b.ID, "")
	if p := s.DependencyProblems(); len(p) != 0 {
		t.Errorf("no circle yet: %v", p)
	}
	s.SetField(b.ID, "after", a.ID, "")
	if p := s.DependencyProblems(); len(p) != 2 {
		t.Errorf("circle: %v", p)
	}
}

func TestClaimRecordsTheOwner(t *testing.T) {
	store := newStore(t)
	created, err := store.Create(NewTask{Area: "work", Title: "担当を記録する"})
	if err != nil {
		t.Fatal(err)
	}
	first := Owner{Label: "local-claude", Session: "aaaa1111"}
	got, changed, err := store.Claim(created.ID, first, "local-claude", "")
	if err != nil || !changed || got.Status != Doing || got.Owner != "local-claude" || got.OwnerSession != "aaaa1111" {
		t.Fatalf("claim = %+v, %v, %v", got, changed, err)
	}
	if _, changed, _ := store.Claim(created.ID, first, "local-claude", ""); changed {
		t.Fatal("claiming one's own task changed it")
	}
	got, changed, err = store.Claim(created.ID, Owner{Label: "work-codex", Session: "bbbb2222"}, "work-codex", "")
	if err != nil || !changed || got.Owner != "work-codex" {
		t.Fatalf("takeover = %+v, %v, %v", got, changed, err)
	}
	log, _ := readTestHistory(t, filepath.Join(store.Root(), got.Dir))
	for _, want := range []string{"担当は local-claude です。（local-claude）", "担当を local-claude から work-codex に替えました。（work-codex）"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// Leaving "doing" leaves the task without an owner.
	got, _, err = store.SetStatus(created.ID, Review, "work-codex", "")
	if err != nil || got.Owner != "" || got.OwnerSession != "" {
		t.Fatalf("after review = %+v, %v", got, err)
	}
	if _, _, err := store.Claim(created.ID, Owner{}, "", ""); err == nil {
		t.Fatal("an unknown owner was accepted")
	}
}

func readTestHistory(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	lines, err := ReadHistory(dir, 0)
	return []byte(strings.Join(lines, "\n")), err
}

func TestBundledRoutineIDs(t *testing.T) {
	for _, id := range []string{"S0001", "S0002", "S0999", "S1001", "S1016", "S1"} {
		if !ValidRoutineID(id) || !validAfterRun(id) {
			t.Errorf("valid ID %q was rejected", id)
		}
	}
	for _, id := range []string{"S0000", "S00001", "S001", "S01", "S0"} {
		if ValidRoutineID(id) || validAfterRun(id) {
			t.Errorf("noncanonical ID %q was accepted", id)
		}
	}
}
