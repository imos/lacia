// Package todo writes TODO.md, the plain-text view of the open tasks for
// readers who are not using the web pages. It is generated; nobody edits it.
package todo

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

var link = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// Render builds the file content from the store.
func Render(store *task.Store, now time.Time) string {
	return render(store, now, nil, nil, nil)
}

func render(store *task.Store, now time.Time, waits map[string]wait.TaskWaits, activity map[string]string, handovers map[string][]handover.Handover) string {
	now = now.In(store.Location())
	type section struct {
		title  string
		status task.Status
	}
	// Tasks of an active project that have not been started are listed under
	// the project, which names the one to start next.
	projects, _ := project.Load(store.Root(), store.Format(), store.Areas())
	inProject := map[string]bool{}
	var plans []project.Plan
	for _, p := range projects {
		if p.Status == project.Active {
			inProject[p.Area+"/"+p.Slug] = true
			plans = append(plans, project.Arrange(p, store.List()))
		}
	}
	sections := []section{
		{"要対応", task.Review},
		{"完了確認待ち", task.Closing},
		{"進行中", task.Doing},
		{"着手待ち", task.Todo},
		{"待機", task.Waiting},
	}
	tasks := store.List()
	var b strings.Builder
	b.WriteString("# やること\n\n")
	b.WriteString("この一覧はタスクの記録から自動で作ります。ここは直接編集せず、タスクの状態は Web の画面か `lacia task set` で変えてください。\n")
	for _, sec := range sections {
		var body strings.Builder
		for _, area := range store.Areas() {
			var lines []string
			for _, t := range tasks {
				if t.Area != area {
					continue
				}
				watched := waits[t.ID]
				if status := t.ListStatus(now, watched.Running(), activity[t.ID] == request.Queued); status != sec.status ||
					(status == task.Todo && inProject[t.Area+"/"+t.Project]) {
					continue
				}
				line := fmt.Sprintf("- [%s](%s/task.md) %s", t.ID, t.Dir, t.Title)
				queued := sec.status == task.Waiting && activity[t.ID] == request.Queued
				running := sec.status == task.Waiting && activity[t.ID] == request.Running
				if queued {
					line += "（会話の順番待ち）"
				}
				if running {
					line += "（会話が依頼を処理しています）"
				}
				if t.Status == task.Doing && t.Owner != "" {
					line += "（担当 " + t.Owner + "）"
				}
				if t.Due != "" {
					line += "（期限 " + t.Due + "）"
				}
				if t.HasReadyConditions() {
					line += "（" + t.ReadyLabel(sec.status == task.Doing).String() + "）"
				}
				if t.ReadyRunRef != "" {
					line += "（[待っていた実行の結果](" + t.ReadyRunRef + ")）"
				}
				for _, item := range watched {
					line += "\n  " + item.Target() + "。" + item.SeenLabel() + "。確認の期限は " + task.FormatWhenIn(item.By, now.Location()) + " です。"
				}
				for _, item := range handovers[t.ID] {
					line += "\n  " + item.Next + " への申し送り。" + item.ConditionText(now)
				}
				liveCondition := queued || running || len(watched) > 0 || len(handovers[t.ID]) > 0
				if t.MissingWaitingCondition(liveCondition) {
					line += "（再開条件が未登録）"
				}
				if t.Status == task.Doing && t.Owner == "" && t.OwnerSession == "" && activity[t.ID] == request.Done && !t.HasReadyConditions() && !liveCondition {
					line += "（受け付けた依頼は終了しました。タスクの状態確認が必要です。）"
				}
				if next := oneLine(t.Next()); next != "" {
					line += "\n  " + next
				}
				lines = append(lines, line)
			}
			if len(lines) > 0 {
				body.WriteString("\n### " + store.AreaLabel(area) + "\n\n" + strings.Join(lines, "\n") + "\n")
			}
		}
		if sec.title == "着手待ち" && len(plans) > 0 {
			b.WriteString("\n## プロジェクト\n\n")
			for _, plan := range plans {
				fmt.Fprintf(&b, "- [%s](%s/project.md)（全%d件のうち、未完了が%d件）\n", plan.Title, plan.Dir, len(plan.Entries), plan.Unfinished())
				if plan.Next != nil {
					fmt.Fprintf(&b, "  次: [%s](%s/task.md) %s", plan.Next.ID, plan.Next.Dir, plan.Next.Title)
					if n := plan.Waiting(); n > 0 {
						fmt.Fprintf(&b, "（そのあと%d件）", n)
					}
					b.WriteString("\n")
				}
			}
		}
		if body.Len() > 0 {
			b.WriteString("\n## " + sec.title + "\n" + body.String())
		}
	}
	return b.String()
}

// oneLine flattens the first paragraph of the "next" section.
func oneLine(md string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(md), "\n\n")
	para = link.ReplaceAllString(para, "$1")
	para = strings.Join(strings.Fields(para), " ")
	if r := []rune(para); len(r) > 140 {
		return string(r[:140]) + "…"
	}
	return para
}

// Write updates TODO.md in the repository root when its content changed.
func Write(store *task.Store, now time.Time) error {
	return write(store, Render(store, now))
}

// WriteLive adds the server's latest observations of requests and watched jobs.
func WriteLive(store *task.Store, now time.Time, waits *wait.Store, requests *request.Queue, handovers *handover.Store) error {
	return write(store, render(store, now, waits.OpenByTask(), requests.TaskActivity(), handovers.ConditionsByTask()))
}

// write leaves a repository that keeps no tasks without TODO.md: there is
// nothing to list, and the file would tell how to move tasks. A file left
// from before is not removed here; lacia doctor points it out.
func write(store *task.Store, content string) error {
	if !store.KeepsTasks() {
		return nil
	}
	path := filepath.Join(store.Root(), "TODO.md")
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
