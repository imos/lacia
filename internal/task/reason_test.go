package task

import (
	"errors"
	"strings"
	"testing"
)

func TestTaskReasonShapeAndSave(t *testing.T) {
	s := newFormat1Store(t)
	item, err := s.Create(NewTask{Area: "work", Title: "きっかけを残す", Status: Doing})
	if err != nil {
		t.Fatal(err)
	}
	summary := "## ひとことで\n\n予定を見やすくします。\n\n"
	reason := "## このタスクを作ったわけ\n\n祝日が分からず、予定を決めにくいことがありました。\n\n"
	next := "## 次にすること\n\nわたしが祝日を表示します。\n"
	for name, c := range map[string]struct {
		body string
		bad  bool
	}{
		"with reason":    {summary + reason + next, false},
		"without reason": {summary + next, false},
		"legacy":         {"## いまの状況\n\n進めます。\n\n" + next, false},
		"before summary": {reason + summary + next, true},
		"after next":     {summary + next + "\n" + reason, true},
		"not adjacent":   {summary + "## 今回したこと\n\n調べました。\n\n" + reason + next, true},
		"duplicate":      {summary + reason + reason + next, true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.WriteDoc(item.ID, TaskFile, c.body, "test")
			if errors.Is(err, ErrShape) != c.bad || (!c.bad && err != nil) {
				t.Fatalf("save: %v", err)
			}
			if got := ShapeProblems(Task{Status: Doing, Body: c.body}, 1); (len(got) > 0) != c.bad {
				t.Fatalf("shape: %v", got)
			}
			if !c.bad {
				got, _ := s.Get(item.ID)
				if got.Body != strings.TrimSpace(c.body)+"\n" || got.Status != Doing || got.Next() != strings.TrimSpace(strings.TrimPrefix(next, "## 次にすること\n\n")) {
					t.Fatalf("read back: %+v", got)
				}
			}
		})
	}
	if got := ShapeProblems(Task{Status: Done, Body: summary + reason}, 1); len(got) != 0 {
		t.Fatalf("closed task: %v", got)
	}
	if got := ShapeProblems(Task{Status: Doing, Body: summary + reason + next}, 0); len(got) == 0 {
		t.Fatal("format 0 unexpectedly accepts format 1 headings")
	}
}

func TestTaskReasonLengthIsAdvice(t *testing.T) {
	s := newFormat1Store(t)
	item, err := s.Create(NewTask{Area: "work", Title: "きっかけの長さ"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"困りました。", "困りました。確かめます！直します？", "困りました。確かめます！直します？伝えます。", "困りました。\n\n```\n例です。例です。例です。例です。\n```"} {
		body := strings.Replace(Body(1, "予定を見やすくします。", "わたしが続けます。"), "## 次にすること", "## このタスクを作ったわけ\n\n"+text+"\n\n## 次にすること", 1)
		wantAdvice := strings.Contains(text, "伝えます。")
		if got := LintFormat(body, 1); (len(got) > 0) != wantAdvice || (wantAdvice && !strings.Contains(got[0].Message, "「このタスクを作ったわけ」が3文")) {
			t.Fatalf("%q: %v", text, got)
		}
		if _, err := s.WriteDoc(item.ID, TaskFile, body, "test"); err != nil {
			t.Fatalf("advice blocked save: %v", err)
		}
	}
}
