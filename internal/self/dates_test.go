package self

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
)

func TestStamp(t *testing.T) {
	previous := "- [2026-11-10] 変わらない項目です。\n- [2026-11-12] 変わる項目です。"
	peer := "- [2026-11-08] 変わらない項目です。\n* [2026-11-09] 相手から来た項目です。"
	// The model moved one day, dropped another and wrote a new item.
	next := "# わたしのこと\n\n- [2026-11-16] 変わらない項目です。\n- 変わる項目です。少し足しました。\n* 相手から来た項目です。\n- 新しい項目です。\n本文の行です。"
	got := stamp(next, "2026-11-17", previous, peer)
	want := "# わたしのこと\n\n- [2026-11-08] 変わらない項目です。\n- [2026-11-17] 変わる項目です。少し足しました。\n* [2026-11-09] 相手から来た項目です。\n- [2026-11-17] 新しい項目です。\n本文の行です。"
	if got != want {
		t.Errorf("stamp =\n%s", got)
	}
	if StripDates(got) != StripDates(next) || strings.Contains(StripDates(got), "[2026") {
		t.Errorf("StripDates =\n%s", StripDates(got))
	}
	if got := carryDates("- 変わる項目です。\n- 知らない項目です。", previous); got != "- [2026-11-12] 変わる項目です。\n- 知らない項目です。" {
		t.Errorf("carryDates = %q", got)
	}
}

func TestBackdate(t *testing.T) {
	k := keeper(t)
	version := func(day, text string) {
		if err := writeFile(filepath.Join(k.Dir, "versions", day+".md"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	version("2026-11-12", "- ずっとある項目です。\n- 1度消えた項目です。\n")
	version("2026-11-13", "- ずっとある項目です。\n")
	version("2026-11-14", "- ずっとある項目です。\n- 1度消えた項目です。\n- [2026-11-14] 途中から載った項目です。\n")
	version("2026-11-15", "- ずっとある項目です。\n- 1度消えた項目です。\n- 途中から載った項目です。\n")
	doc := "- ずっとある項目です。\n- 1度消えた項目です。\n- 途中から載った項目です。\n- どの版にもない項目です。\n- [2026-11-01] 日付のある項目です。"
	want := "- [2026-11-12] ずっとある項目です。\n- [2026-11-14] 1度消えた項目です。\n- [2026-11-14] 途中から載った項目です。\n- [2026-11-17] どの版にもない項目です。\n- [2026-11-01] 日付のある項目です。"
	if got := k.backdate(doc, "2026-11-17"); got != want {
		t.Errorf("backdate =\n%s", got)
	}
}

// A document from before the days were kept gets them on its first night,
// and the checks do not count them.
func TestSleepKeepsDates(t *testing.T) {
	k := keeper(t)
	if err := writeFile(k.Path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(k.Dir, "versions", "2026-11-15.md"), []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := ""
	k.Think = func(_ context.Context, p string) (string, error) {
		prompt = p
		return answer(strings.Replace(first, "- 比較の報告では", "- [2020-01-01] 新しく気づいたことです。\n- 比較の報告では", 1), "1項目を足しました。"), nil
	}
	k.AddNote(Note{Text: "気づいたことです。"})
	if night, err := k.Sleep(context.Background(), false); err != nil || !night.Accepted {
		t.Fatalf("night = %+v, %v", night, err)
	}
	if !strings.Contains(prompt, "- [2026-11-15] 結論を先に聞きたい方だと考えています。") {
		t.Error("the prompt lacks the day of an old item")
	}
	for _, want := range []string{"- [2026-11-15] 結論を先に聞きたい方だと考えています。", "- [2026-11-17] 新しく気づいたことです。", "- [2026-11-15] 比較の報告では"} {
		if !strings.Contains(k.Document(), want) {
			t.Errorf("the document lacks %q:\n%s", want, k.Document())
		}
	}
	// The length is counted without the days.
	k.Lines = 1
	if err := k.check("", "# わたしのこと\n## わたし自身\n## 仕事モードで\n", false); err == nil {
		t.Error("a document over the limit of lines passed")
	}
	k.Lines = 0
}

func TestImprovements(t *testing.T) {
	k := keeper(t)
	out := answer(first, "変えていません。") + `<仕組みで直せそうなこと>
- 題名: 家族との場で書き留めのきっかけを作る
  もとになった項目: 昼のあいだに書き留めてみたい、という項目です。
  理由：何晩も続けて書き留めが残っていません。
  直し方の見当: 会話の始めの決まりに1行足します。
- 題名: 2つめの候補
- 題名: 3つめの候補
</仕組みで直せそうなこと>
`
	if got := parseImprovements(answer(first, "") + "<仕組みで直せそうなこと>\nなし\n</仕組みで直せそうなこと>"); len(got) != 0 {
		t.Errorf("none = %+v", got)
	}
	// Off unless the profile asks for it.
	k.Think = func(context.Context, string) (string, error) { return out, nil }
	k.AddNote(Note{Text: "気づいたことです。"})
	k.Sleep(context.Background(), false)
	if rule, output, known := k.improvePrompt(); rule+output+known != "" || len(k.Improvements()) != 0 {
		t.Error("candidates were kept without the setting")
	}
	if got := k.prompt(k.now(), first, nil, nil, "", false, ""); strings.Contains(got, "仕組みで直せそうなこと") || strings.Contains(got, "{{IMPROVE") {
		t.Error("the prompt asks for candidates without the setting")
	}

	var notices []string
	k.ImproveTell = func(notice, text string) error { notices = append(notices, text); return nil }
	k.TaskState = func(id string) string { return "完了" }
	k.AddNote(Note{Text: "気づいたことです。"})
	if night, _ := k.Sleep(context.Background(), false); !night.Accepted {
		t.Fatalf("night = %+v", night)
	}
	list := k.Improvements()
	if len(list) != 2 || list[0].ID != "K1" || list[0].Day != "2026-11-17" || list[0].Reason != "何晩も続けて書き留めが残っていません。" || list[0].Fix == "" || list[1].Title != "2つめの候補" {
		t.Fatalf("improvements = %+v", list)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "## K1　家族との場で書き留めのきっかけを作る") || !strings.Contains(notices[0], "lacia self improve <候補のID> task <タスクのID>") ||
		!strings.Contains(notices[0], "確認待ちにして"+task.OwnerName+"に見ていただきます") || strings.Contains(notices[0], "もう1度知らせます") {
		t.Errorf("notice = %v", notices)
	}
	if _, err := k.DecideImprovement("K1", ImproveSkipped, "", ""); err == nil {
		t.Error("a candidate was skipped without a reason")
	}
	if _, err := k.DecideImprovement("K9", ImproveTask, "T1", ""); err == nil {
		t.Error("an unknown candidate was settled")
	}
	if got, err := k.DecideImprovement("k1", ImproveTask, "T1250", ""); err != nil || got.Task != "T1250" {
		t.Errorf("decide = %+v, %v", got, err)
	}
	// The same titles are not kept twice; the open one is told again.
	k.AddNote(Note{Text: "気づいたことです。"})
	k.Sleep(context.Background(), false)
	if len(k.Improvements()) != 2 || len(notices) != 2 || strings.Contains(notices[1], "K1") || !strings.Contains(notices[1], "## K2") || !strings.Contains(notices[1], "もう1度知らせます") {
		t.Errorf("second night: %d candidates, notices = %v", len(k.Improvements()), notices)
	}
	k.DecideImprovement("K2", ImproveSkipped, "", "願いごとでした。")
	got := k.prompt(k.now(), first, nil, nil, "", false, "")
	for _, want := range []string{"## 仕組みで直せそうなこと", "<仕組みで直せそうなこと>", "### これまでに出した候補", "タスク T1250 になりました。いまの状態は「完了」です。", "見送りました。願いごとでした。"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	k.AddNote(Note{Text: "気づいたことです。"})
	k.Sleep(context.Background(), false)
	if len(notices) != 2 {
		t.Error("a notice went out with nothing open")
	}
	if _, err := os.Stat(k.improvePath()); err != nil {
		t.Error(err)
	}
}

func TestDateStampsNowAndKeepsText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "self.md")
	doc := "# わたしのこと\n\n## わたし自身\n\n- 古い項目です。\n- 新しい項目です。\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state", "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "versions", "2026-11-20.md"), []byte("- 古い項目です。\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := &Keeper{Path: path, Dir: filepath.Join(dir, "state"), Now: func() time.Time { return time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC) }}
	changed, err := k.Date()
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := k.Document()
	if !strings.Contains(got, "- [2026-11-20] 古い項目です。") || !strings.Contains(got, "- [2026-11-27] 新しい項目です。") {
		t.Fatalf("unexpected document:\n%s", got)
	}
	if StripDates(strings.TrimSpace(got)) != strings.TrimSpace(doc) {
		t.Fatalf("text changed:\n%s", got)
	}
	if changed, _ := k.Date(); changed {
		t.Fatal("second run should change nothing")
	}
}
