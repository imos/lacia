package schedule

import (
	"strings"
	"testing"
)

func TestMattersTaskNamesAnotherRoutine(t *testing.T) {
	definition := func(id, extra string) string {
		return "---\nid: " + id + "\n" + extra + "daily: 09:00\n---\n確認します。"
	}
	for _, c := range []struct {
		name, source, target, want string
	}{
		{name: "valid", source: "matters_task: S1012\n"},
		{name: "missing", source: "matters_task: S1099\n", want: "見つかりません"},
		{name: "self", source: "matters_task: S1013\n", want: "自身は指定できません"},
		{name: "chain", source: "matters_task: S1012\n", target: "matters_task: S1014\n", want: "さらに別の定期タスク（S1014）"},
		{name: "not an ID", source: "matters_task: mail\n", want: "定期タスクのIDを指定"},
		{name: "other output", source: "output: task\nmatters_task: S1012\n", want: "output: matters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := newRecords(t)
			putDefinition(t, root, "areas/work/routines/S1012/routine.md", definition("S1012", c.target))
			putDefinition(t, root, "areas/work/routines/S1013/routine.md", definition("S1013", c.source))
			putDefinition(t, root, "areas/work/routines/S1014/routine.md", definition("S1014", ""))
			jobs := Load(root)
			if len(jobs) != 3 || jobs[1].ID != "S1013" {
				t.Fatalf("jobs: %+v", jobs)
			}
			source := jobs[1]
			if c.want == "" {
				if source.Err != "" || !source.Enabled || source.MattersTask != "S1012" {
					t.Fatalf("valid definition: %+v", source)
				}
			} else if !strings.Contains(source.Err, c.want) || source.Enabled {
				t.Fatalf("want %q in %q (enabled %v)", c.want, source.Err, source.Enabled)
			}
			if jobs[2].Err != "" || jobs[2].MattersTask != "" {
				t.Fatalf("a routine without the field changed: %+v", jobs[2])
			}
		})
	}
}

func TestReportInstructionNamesMattersAsTheDefault(t *testing.T) {
	job := Job{ID: "S1013", Name: "S1013", Output: "matters", ExplicitMatters: true}
	before := job.ReportInstruction("run-id", false)
	if !strings.Contains(before, "通常のタスクで用件を知らせる手順の場合は") || strings.Contains(before, "通常のタスクは新しく作りません") {
		t.Fatalf("the wording changed where matters are not collected: %s", before)
	}
	got := job.ReportInstruction("run-id", true)
	for _, want := range []string{"lacia run report S1013 run-id", "サーバーが、matters の用件を定期タスクのタスクへ集めます", "通常のタスクは新しく作りません",
		"handoffs を使うのは、同じ用件をすでに追っている通常のタスクがあるときと、定義の「すること」が通常のタスクを作ると名前を挙げて書いているときだけです"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, gone := range []string{"通常のタスクで用件を知らせる手順の場合は", "用件をどこへ保存して知らせるかが本文や手順で決まっていれば"} {
		if strings.Contains(got, gone) {
			t.Errorf("still says %q", gone)
		}
	}
	if strings.Index(got, "matters の各項目") > strings.Index(got, "handoffs を使うのは") {
		t.Error("matters must come before handoffs")
	}
	task := Job{ID: "S1008", Name: "S1008", Output: "task", ExplicitMatters: true}
	if task.ReportInstruction("run-id", true) != task.ReportInstruction("run-id", false) {
		t.Error("a routine whose output is a task changed")
	}
}
