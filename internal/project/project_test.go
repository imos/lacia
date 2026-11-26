package project

import (
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

const sample = `---
title: 金融特化LLM
status: active
---

## 目的

金融の質問に強いモデルを作ります。

## 順序

1. X58 — 定義が決まらないと比較ができないため
2. X61（前提: X58）
3. X62（前提: X58, X61）— 学習にはデータが要るため
4. X99

## 資料

- [計画](plan.md)
`

func tasks() []task.Task {
	mk := func(id string, status task.Status, project string, blockers ...string) task.Task {
		return task.Task{ID: id, Title: id + " の題名", Status: status, Area: "work", Project: project, Created: "2026-11-01", After: blockers, Blockers: blockers}
	}
	return []task.Task{
		mk("X25", task.Done, "alpha-model"), mk("X58", task.Todo, "alpha-model"), mk("X61", task.Todo, "alpha-model", "X58"),
		mk("X62", task.Todo, "alpha-model", "X58", "X61"), mk("X63", task.Todo, "alpha-model"), mk("X70", task.Todo, ""),
	}
}

func TestParseAndArrange(t *testing.T) {
	p, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	p.Slug, p.Area = "alpha-model", "work"
	if len(p.Order) != 4 || p.Order[2].ID != "X62" || p.Order[2].Reason != "学習にはデータが要るため" {
		t.Fatalf("order = %+v", p.Order)
	}
	plan := Arrange(p, tasks())
	var ids []string
	for _, e := range plan.Entries {
		ids = append(ids, e.ID)
	}
	// Ordered tasks first, then the ones the order does not mention.
	if got := strings.Join(ids, " "); got != "X58 X61 X62 X25 X63" {
		t.Errorf("entries = %s", got)
	}
	if plan.Next == nil || plan.Next.ID != "X58" || plan.Closed != 1 || plan.Waiting() != 3 {
		t.Errorf("next %v, closed %d, waiting %d", plan.Next, plan.Closed, plan.Waiting())
	}
	if strings.Join(plan.Entries[2].Blockers, ",") != "X58,X61" || !plan.Entries[4].Unordered || plan.Entries[3].Unordered {
		t.Errorf("blockers %v, unordered %v", plan.Entries[2].Blockers, plan.Entries[4].Unordered)
	}
	if strings.Join(plan.Missing, ",") != "X99" {
		t.Errorf("missing = %v", plan.Missing)
	}

	// With X58 closed, X61 is next; X62 still waits for X61.
	ts := tasks()
	ts[1].Status = task.Done
	ts[2].Blockers, ts[3].Blockers = nil, []string{"X61"}
	plan = Arrange(p, ts)
	if plan.Next.ID != "X61" || strings.Join(plan.Entries[2].Blockers, ",") != "X61" {
		t.Errorf("next %s, blockers %v", plan.Next.ID, plan.Entries[2].Blockers)
	}
	// A blocked task is skipped for the next one that can start.
	ts[2].Status = task.Doing
	if plan = Arrange(p, ts); plan.Next.ID != "X63" {
		t.Errorf("next = %s", plan.Next.ID)
	}
}

func TestMove(t *testing.T) {
	all := []string{"X58", "X61", "X62", "X63"}
	out, err := Move(sample, all, "X62", -1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1. X58 — 定義が決まらないと比較ができないため\n2. X62 — 学習にはデータが要るため\n3. X61\n4. X99\n5. X63\n") {
		t.Errorf("moved:\n%s", out)
	}
	if !strings.Contains(out, "## 資料\n\n- [計画](plan.md)") || !strings.Contains(out, "金融の質問に強いモデルを作ります。") {
		t.Errorf("other sections changed:\n%s", out)
	}
	if _, err := Move(sample, all, "X70", 1); err == nil {
		t.Error("a task outside the project was moved")
	}
	if same, _ := Move(sample, []string{"X58"}, "X58", -1); !strings.Contains(same, "1. X58") {
		t.Errorf("moving the first one up:\n%s", same)
	}
}
