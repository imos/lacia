package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDocReplacesTheBodyAndKeepsTheFrontMatter(t *testing.T) {
	s := newLayoutStore(t)
	got, err := s.Create(NewTask{Area: "work", Title: "本文を直接保存する", Status: Review, Actor: "work-codex"})
	if err != nil {
		t.Fatal(err)
	}
	body := "## ひとことで\n\n本文を、会話を通さずに保存できることを確かめるタスクです。\n\n## 次にすること\n\n" + OwnerName + "に、結果を見ていただきます。\n"
	// CRLF and the space around the text are tidied; the body is otherwise
	// saved as it was given.
	written, err := s.WriteDoc(got.ID, TaskFile, "\n"+strings.ReplaceAll(body, "\n", "\r\n")+"\n\n", "local-claude")
	if err != nil {
		t.Fatal(err)
	}
	if written.Body != body || written.Status != Review || written.Title != got.Title {
		t.Fatalf("written = %+v", written)
	}
	data, _ := os.ReadFile(filepath.Join(s.Root(), got.Dir, TaskFile))
	if want := "---\nid: " + got.ID + "\ntitle: 本文を直接保存する\nstatus: review\ncreated: 2026-11-24\n---\n\n" + body; string(data) != want {
		t.Errorf("task.md =\n%s\nwant\n%s", data, want)
	}
	lines, _ := s.HistoryLines(written, 1)
	if len(lines) != 1 || !strings.Contains(lines[0], "task.md を書き換えました") || !strings.HasSuffix(lines[0], "（local-claude）") {
		t.Errorf("history = %q", lines)
	}
	// Replacing the body is not a handover: the status did not change.
	if all, _ := s.HistoryLines(written, 0); len(all) != 2 {
		t.Errorf("history has %d lines: %q", len(all), all)
	}
	events, _ := ReadEvents(filepath.Join(s.Root(), got.Dir, HistoryFile))
	if e := events[len(events)-1].Event; e.Type != EventFile || e.Action != "wrote" || e.Path != TaskFile || e.SHA256 == "" || e.Bytes == nil || *e.Bytes != int64(len(body)) {
		t.Errorf("event = %+v", e)
	}
}

func TestWriteDocRefusesTextThatBreaksTheShape(t *testing.T) {
	s := newLayoutStore(t)
	got, _ := s.Create(NewTask{Area: "work", Title: "形を守る"})
	before, _ := os.ReadFile(filepath.Join(s.Root(), got.Dir, TaskFile))
	good := "## ひとことで\n\n形を確かめます。\n\n## 次にすること\n\nわたしが確かめます。\n"
	for name, c := range map[string]struct{ file, content, want string }{
		"front matter":    {TaskFile, "---\nid: " + got.ID + "\nstatus: done\n---\n\n" + good, "冒頭の項目"},
		"unknown heading": {TaskFile, good + "\n## 経緯\n\n長い話です。\n", "「経緯」という見出し"},
		"heading level":   {TaskFile, "# 題名\n\n" + good, "書式が違います"},
		"no next step":    {TaskFile, "## ひとことで\n\n形を確かめます。\n", "「## 次にすること」がありません"},
		"notes as task":   {TaskFile, "# " + got.ID + " の補足\n\n## ここまでできたら完了\n\n確かめたら完了です。\n", "書式が違います"},
		"task as notes":   {NotesFile, good, "1行目"},
		"empty":           {TaskFile, " \n", "空です"},
		"another file":    {"log.jsonl", "{}", "task.md か notes.md"},
		"a path":          {"../T1002/task.md", good, "task.md か notes.md"},
		"too large":       {NotesFile, "# 補足\n\n" + strings.Repeat("あ", MaxDocSize), "超えています"},
	} {
		_, err := s.WriteDoc(got.ID, c.file, c.content, "local-claude")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if _, err := s.WriteDoc(got.ID, TaskFile, "## 経緯\n\n話です。\n", ""); !errors.Is(err, ErrShape) {
		t.Errorf("a refused shape is not ErrShape: %v", err)
	}
	if _, err := s.WriteDoc("T9999", NotesFile, "# 補足\n", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing task: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(s.Root(), got.Dir, TaskFile))
	if string(after) != string(before) {
		t.Errorf("a refused write changed task.md:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), got.Dir, NotesFile)); !os.IsNotExist(err) {
		t.Errorf("a refused write left notes.md: %v", err)
	}
	if lines, _ := s.HistoryLines(got, 0); len(lines) != 1 {
		t.Errorf("refused writes were logged: %q", lines)
	}
}

func TestWriteDocWritesNotes(t *testing.T) {
	s := newLayoutStore(t)
	got, _ := s.Create(NewTask{Area: "work", Title: "補足を書く", Status: Done})
	notes := "# " + got.ID + " の補足\n\n## ここまでできたら完了\n\n確かめたら完了です。\n"
	written, err := s.WriteDoc(got.ID, NotesFile, notes, "local-claude")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(s.Root(), got.Dir, NotesFile)); string(data) != notes {
		t.Errorf("notes.md = %q", data)
	}
	if written.Status != Done {
		t.Errorf("status = %s", written.Status)
	}
	if lines, _ := s.HistoryLines(got, 1); !strings.Contains(lines[0], "notes.md を書き換えました") {
		t.Errorf("history = %q", lines)
	}
	// A closed task needs no next step, so its body may leave the section out.
	if _, err := s.WriteDoc(got.ID, TaskFile, "## ひとことで\n\n補足を書いて、終えました。\n", "local-claude"); err != nil {
		t.Errorf("a closed task without a next step: %v", err)
	}
}
