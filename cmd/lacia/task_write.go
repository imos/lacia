package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func (c *taskCommand) createTask() error {
	if c.api != "" {
		var t task.Task
		if err := c.api.call("POST", "/api/tasks", map[string]string{"area": *c.area, "title": *c.title, "status": *c.status, "parent": *c.parent, "slug": *c.slug, "actor": c.actor}, &t); err != nil {
			return err
		}
		fmt.Printf("%s を作りました: %s/task.md\n", t.ID, t.Dir)
		return nil
	}
	t, err := c.store.Create(task.NewTask{Area: *c.area, Title: *c.title, Status: task.Status(*c.status), Parent: *c.parent, Slug: *c.slug, Actor: c.actor})
	if err != nil {
		return err
	}
	fmt.Printf("%s を作りました: %s/task.md\n", t.ID, t.Dir)
	return nil
}

func (c *taskCommand) setStatus() error {
	if err := c.need(2, "IDと状態"); err != nil {
		return err
	}
	var t task.Task
	var changed bool
	var waitingWarning string
	if c.api != "" {
		var out updateReply
		if err := c.api.call("POST", "/api/tasks/"+c.positional[0], map[string]string{"status": c.positional[1], "note": *c.note, "actor": c.actor}, &out); err != nil {
			return err
		}
		t, changed = out.Task, out.Changed
		out.warn()
		waitingWarning = out.WaitingWarning
	} else {
		var err error
		if before, _ := c.store.Get(c.positional[0]); c.positional[1] != string(task.Doing) || c.owner.Session == "" {
			t, changed, err = c.store.SetStatus(c.positional[0], task.Status(c.positional[1]), c.actor, *c.note)
		} else if before.Status == task.Doing && before.OwnerSession != "" && before.OwnerSession != c.owner.Session {
			t = before
			warnOwned(before)
		} else {
			t, changed, err = c.store.Claim(c.positional[0], c.owner, c.actor, *c.note)
		}
		if err != nil {
			return err
		}
		if t.MissingWaitingCondition(false) {
			live, err := localWaitConditions(*c.state, true)
			if err != nil {
				fmt.Fprintln(os.Stderr, "再開条件の登録状況を確かめられませんでした。", err)
			} else if !live[t.ID] {
				waitingWarning = task.WaitingConditionWarning
			}
		}
	}
	if waitingWarning != "" {
		printWaitingGuidance(t.ID, waitingWarning)
	}
	if !changed {
		fmt.Printf("%s はすでに「%s」です。\n", t.ID, t.Status.Label())
		return nil
	}
	fmt.Printf("%s を「%s」にしました。\n", t.ID, t.Status.Label())
	if line := closingReminder(t); line != "" {
		fmt.Println(line)
	}
	return nil
}

func (c *taskCommand) claimTask() error {
	if err := c.need(1, "ID"); err != nil {
		return err
	}
	var t task.Task
	var changed bool
	if c.api != "" {
		var out updateReply
		if err := c.api.call("POST", "/api/tasks/"+c.positional[0], map[string]any{"claim": true, "note": *c.note, "actor": c.actor}, &out); err != nil {
			return err
		}
		t, changed = out.Task, out.Changed
	} else {
		var err error
		if c.owner.Session == "" {
			return errors.New("担当にする会話が分かりません。Codex か Claude の中から実行してください")
		}
		if t, changed, err = c.store.Claim(c.positional[0], c.owner, c.actor, *c.note); err != nil {
			return err
		}
	}
	if !changed {
		fmt.Printf("%s は、すでにこの会話（%s）が進めています。\n", t.ID, t.Owner)
		return nil
	}
	fmt.Printf("%s の担当を、この会話（%s）にしました。\n", t.ID, t.Owner)
	return nil
}

func (c *taskCommand) setField() error {
	if err := c.need(2, "IDと項目"); err != nil {
		return err
	}
	value := strings.Join(c.positional[2:], " ")
	if c.api != "" {
		var out updateReply
		if err := c.api.call("POST", "/api/tasks/"+c.positional[0], map[string]string{"field": c.positional[1], "value": value, "actor": c.actor}, &out); err != nil {
			return err
		}
		out.warn()
	} else {
		if c.positional[1] == "after_run" {
			var err error
			value, err = schedule.ResolveWait(c.dir, value)
			if err != nil {
				return err
			}
		}
		if _, _, err := c.store.SetField(c.positional[0], c.positional[1], value, c.actor); err != nil {
			return err
		}
	}
	fmt.Printf("%s の %s を更新しました。\n", strings.ToUpper(c.positional[0]), c.positional[1])
	return nil
}

func (c *taskCommand) setRecheck() error {
	// Registers how often and until when a waiting task is looked at.
	// The server keeps the time of the next look.
	if err := c.need(1, "ID"); err != nil {
		return err
	}
	var t task.Task
	var changed bool
	if c.api != "" {
		var out updateReply
		if err := c.api.call("POST", "/api/tasks/"+c.positional[0], map[string]any{"recheck": map[string]string{"every": *c.every, "until": *c.until, "next": *c.next}, "actor": c.actor}, &out); err != nil {
			return err
		}
		t, changed = out.Task, out.Changed
		out.warn()
	} else {
		var err error
		if t, changed, err = c.store.SetRecheck(c.positional[0], *c.every, *c.until, *c.next, c.actor); err != nil {
			return err
		}
	}
	if !changed {
		fmt.Printf("%s には、同じ確認がすでに登録されています。\n", t.ID)
		return nil
	}
	fmt.Printf("%s を外部待ちにしました。確認の予定は「%s」です。最初の確認は %s です。\n", t.ID, t.RecheckLabel(), t.ResumeOn)
	fmt.Println("「次にすること」に、何を確かめるのかと、何が起きたらお知らせするのかを書いてください。確認を担当する会話は、その記述をもとに確かめます。")
	return nil
}

func (c *taskCommand) recordRecheck() error {
	// Records what one look at a waiting task found.
	if err := c.need(3, "ID、結果（same、changed、unknown のどれか）、何を確かめてどうだったのか"); err != nil {
		return err
	}
	text := strings.Join(c.positional[2:], " ")
	var t task.Task
	if c.api != "" {
		var out updateReply
		if err := c.api.call("POST", "/api/tasks/"+c.positional[0], map[string]string{"rechecked": c.positional[1], "text": text, "actor": c.actor}, &out); err != nil {
			return err
		}
		t = out.Task
		out.warn()
	} else {
		var err error
		if t, _, err = c.store.Rechecked(c.positional[0], c.positional[1], text, c.actor); err != nil {
			return err
		}
	}
	switch c.positional[1] {
	case task.RecheckChanged:
		fmt.Printf("%s の結果を「変化あり」と記録して、確認待ちにしました。繰り返しの確認は止まりました。\n", t.ID)
	case task.RecheckUnknown:
		fmt.Printf("%s の結果を「確かめられなかった」と記録しました。外部待ちを続けます。\n%s\n", t.ID, t.RecheckState())
	default:
		fmt.Printf("%s の結果を「変化なし」と記録しました。外部待ちを続けます。\n%s\n", t.ID, t.RecheckState())
	}
	return nil
}

func (c *taskCommand) attachFiles() error {
	if err := c.need(2, "IDと、置くファイルかディレクトリー"); err != nil {
		return err
	}
	// A directory is sent with its files under its own name, so that an
	// HTML page keeps working with the images and scripts next to it.
	type item struct{ path, name string }
	var items []item
	for _, src := range c.positional[1:] {
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			name := filepath.Base(src)
			if *c.as != "" && len(c.positional) == 2 {
				name = *c.as
			}
			items = append(items, item{src, name})
			continue
		}
		prefix := filepath.Base(filepath.Clean(src))
		if *c.as != "" && len(c.positional) == 2 {
			prefix = *c.as
		}
		err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			items = append(items, item{p, filepath.ToSlash(filepath.Join(prefix, rel))})
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, it := range items {
		f, err := os.Open(it.path)
		if err != nil {
			return err
		}
		if c.api != "" {
			err = c.api.put("/api/tasks/"+c.positional[0]+"/files?"+url.Values{"to": {*c.to}, "name": {it.name}, "actor": {c.actor}}.Encode(), f)
		} else {
			_, err = c.store.PutFile(c.positional[0], *c.to, it.name, f, c.actor)
		}
		f.Close()
		if err != nil {
			return err
		}
		fmt.Printf("%s/%s を置きました。\n", *c.to, it.name)
	}
	remindToResume(c.api, c.store, c.positional[0])
	return nil
}

func (c *taskCommand) addNote() error {
	if err := c.need(2, "タスクのIDと履歴に書く文章"); err != nil {
		return err
	}
	entry := strings.Join(c.positional[1:], " ")
	if c.api != "" {
		var out updateReply
		if err := c.api.call("POST", "/api/tasks/"+c.positional[0], map[string]string{"log": entry, "actor": c.actor}, &out); err != nil {
			return err
		}
		out.warn()
	} else if err := c.store.Log(c.positional[0], task.Event{Type: task.EventNote, By: noteActor(c.actor), Text: strings.TrimSpace(entry)}); err != nil {
		return err
	}
	fmt.Println("履歴に書きました。")
	remindToResume(c.api, c.store, c.positional[0])
	return nil
}

func (c *taskCommand) writeDoc() error {
	// Replaces the body of task.md, or notes.md, with a file or with the
	// standard input. On a read-only copy this is how text is saved: the
	// server writes it, and no conversation has to type it in.
	if err := c.need(2, "タスクのIDと保存先のファイル名（task.md か notes.md）"); err != nil {
		return err
	}
	var content []byte
	var err error
	if len(c.positional) > 2 && c.positional[2] != "-" {
		content, err = os.ReadFile(c.positional[2])
	} else {
		content, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	var t task.Task
	var advice []string
	if c.api != "" {
		var out struct {
			updateReply
			Advice []string
		}
		if err := c.api.putOut("/api/tasks/"+url.PathEscape(c.positional[0])+"/doc?"+url.Values{"name": {c.positional[1]}, "actor": {c.actor}}.Encode(), bytes.NewReader(content), &out); err != nil {
			return err
		}
		t, advice = out.Task, out.Advice
		out.warn()
	} else {
		if before, _ := c.store.Get(c.positional[0]); before.Status == task.Doing && before.OwnerSession != "" && c.owner.Session != "" && before.OwnerSession != c.owner.Session {
			warnOwned(before)
		}
		if t, err = c.store.WriteDoc(c.positional[0], c.positional[1], string(content), c.actor); err != nil {
			return err
		}
		if c.positional[1] == task.TaskFile {
			for _, w := range append(task.Check(t, c.store.Files(t)), task.Lint(t.Body)...) {
				advice = append(advice, w.String())
			}
		}
	}
	if c.api == "" && c.positional[1] == task.NotesFile {
		for _, w := range task.LintDocument("notes", string(content)) {
			advice = append(advice, w.String())
		}
	}
	fmt.Printf("%s の %s を保存しました。\n", t.ID, c.positional[1])
	for _, line := range advice {
		fmt.Printf("%s: %s\n", t.ID, line)
	}
	if line := resumeReminder(t); line != "" {
		fmt.Println(line)
	}
	return nil
}
