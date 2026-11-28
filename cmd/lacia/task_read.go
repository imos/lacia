package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/task"
)

func (c *taskCommand) showTask() error {
	if err := c.need(1, "ID"); err != nil {
		return err
	}
	t, err := c.store.Get(c.positional[0])
	if err != nil {
		return err
	}
	detail, err := c.store.Detail(t)
	if err != nil {
		return err
	}
	if *c.asJSON {
		return json.NewEncoder(os.Stdout).Encode(detail)
	}
	printTaskDetail(detail)
	return nil
}

func (c *taskCommand) printHistory() error {
	if err := c.need(1, "ID"); err != nil {
		return err
	}
	t, err := c.store.Get(c.positional[0])
	if err != nil {
		return err
	}
	if *c.asJSON {
		events, err := task.ReadEvents(filepath.Join(c.dir, t.Dir, task.HistoryFile))
		if err != nil {
			return err
		}
		if *c.tail > 0 && len(events) > *c.tail {
			events = events[len(events)-*c.tail:]
		}
		for _, e := range events {
			fmt.Println(e.Raw)
		}
		return nil
	}
	lines, err := c.store.HistoryLines(t, *c.tail)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

func (c *taskCommand) lint() error {
	context, err := stateLintContext(*c.state)
	if err != nil {
		return err
	}
	byID := map[string]task.Task{}
	for _, t := range c.store.List() {
		byID[t.ID] = t
	}
	var targets []task.Task
	if len(c.positional) == 0 {
		for _, t := range c.store.List() {
			if !t.Status.Closed() {
				targets = append(targets, t)
			}
		}
	}
	for _, ref := range c.positional {
		t, err := c.store.Get(ref)
		if err != nil {
			return err
		}
		targets = append(targets, t)
	}
	waitingWarnings, waitingErr := lintWaitingConditions(c.api, *c.state, targets)
	count := 0
	if waitingErr != nil {
		fmt.Println("再開条件の登録状況を確かめられませんでした。", waitingErr)
		count++
	}
	for _, t := range targets {
		if advice := waitingWarnings[t.ID]; advice != "" {
			fmt.Printf("%s: %s\n", t.ID, advice)
			count++
		}
		for _, w := range task.StateWarnings(t, byID, context) {
			fmt.Printf("%s: %s\n", t.ID, w)
			count++
		}
		for _, w := range append(task.Check(t, c.store.Files(t)), task.Lint(t.Body)...) {
			fmt.Printf("%s: %s\n", t.ID, w)
			count++
		}
		for _, w := range task.LintNotesFile(filepath.Join(c.dir, t.Dir, "notes.md")) {
			fmt.Printf("%s notes.md: %s\n", t.ID, w)
			count++
		}
		for _, key := range t.Unknown {
			if strings.HasPrefix(key, "x_") {
				continue
			}
			fmt.Printf("%s: 冒頭の %s は定義されていない項目です。独自に加える項目の名前は x_ で始めてください。\n", t.ID, key)
			count++
		}
	}
	if len(c.positional) == 0 {
		for _, problem := range c.store.DependencyProblems() {
			fmt.Println(problem)
			count++
		}
	}
	for _, t := range targets {
		for _, id := range t.After {
			if _, err := c.store.Get(id); err != nil {
				fmt.Printf("%s: after にある %s というタスクはありません。このままでは着手できません。\n", t.ID, id)
				count++
			}
		}
	}
	if count == 0 {
		fmt.Println("気になる箇所はありませんでした。")
	}
	return nil
}

func (c *taskCommand) printPath() error {
	if err := c.need(1, "ID"); err != nil {
		return err
	}
	t, err := c.store.Get(c.positional[0])
	if err != nil {
		return err
	}
	fmt.Println(filepath.Join(c.dir, t.Dir))
	return nil
}
