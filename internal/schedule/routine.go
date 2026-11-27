package schedule

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/task"
)

var ErrRoutineReport = errors.New("この定期タスクの実行回に結果を保存できません")

// OccurrenceName keeps existing dispatch IDs and slot boundaries when a job
// is moved from its former filename to an S-series definition.
func (j Job) OccurrenceName() string {
	if j.LegacyName != "" {
		return j.LegacyName
	}
	return j.Name
}

// NormalizeCoverage supplies the whole-routine scope for a simple full check.
func (j Job) NormalizeCoverage(report *routine.Result) error {
	if j.ExplicitMatters && len(j.CheckScopes) == 0 && len(report.Checks) == 0 && report.CheckStatus == "complete" {
		report.Checks = []routine.Check{{Scope: "routine/" + j.Name, Status: "verified"}}
	}
	return report.NormalizeChecks()
}

// ReportInstruction reuses one result channel for findings and check coverage.
// collected says that the server gathers the matters of this run into a
// routine's task; the wording then names matters as where findings go, and
// keeps handoffs for the two cases where an ordinary task is the right place.
func (j Job) ReportInstruction(runID string, collected bool) string {
	if j.ID == "" && !j.CheckResult {
		return ""
	}
	if j.Output != "matters" && !j.CheckResult && !(j.ExplicitMatters && j.Output == "task") {
		return ""
	}
	id := j.ID
	if id == "" {
		id = j.Name
	}
	collected = collected && j.ID != "" && j.Output == "matters"
	text := fmt.Sprintf("\n\nこの定期タスクのIDは %s で、この回の実行IDは %s です。結果を lacia run report %s %s <JSONファイル> で保存してください。\n", id, runID, id, runID)
	if j.ExplicitMatters {
		text += `結果はJSONで書きます。予定した範囲を確認できたら check_status を complete にし、確認結果や件数は summary に書きます。要約だけなら実行履歴に残ります。
`
		if collected {
			text += task.OwnerName + `にお願いすることや、この回が終わっても対応が必要な用件は、matters に1件ずつ書きます。matters の各項目には key と revision を入れ、お願いすることを summary に具体的に書きます。同じ用件の同じ更新には同じ key と revision を使います。サーバーが、matters の用件を定期タスクのタスクへ集めます。通常のタスクは新しく作りません。
handoffs を使うのは、同じ用件をすでに追っている通常のタスクがあるときと、定義の「すること」が通常のタスクを作ると名前を挙げて書いているときだけです。そのときは、通常のタスクへの記録を読み戻してから、handoffs の各項目に key、revision、summary、task_id を書きます。
`
		} else {
			text += `通常のタスクで用件を知らせる手順の場合は、そこへの記録を読み戻してから、handoffs の各項目に key、revision、summary、task_id を書きます。
`
		}
		text += `一部だけ確認できたときは check_status を partial にし、どの範囲も確認できなかったときは unavailable にします。
checks には、確認する範囲ごとに scope と status を書きます。確認できた範囲には verified を指定します。確認できなかった範囲には unverified を指定し、reason に理由を添えます。原因が同じだと確かめたときだけ、同じ cause_key を使います。
通常のタスクに記録できなかったときも、その範囲と理由を残します。どの範囲も読めず、認証などの操作が必要と分かったときは、手順で決めた方法でその操作を案内します。秘密の値やコマンドの全出力は書きません。返信先へのお知らせは、定期タスクの本文に従います。
`
		switch {
		case collected:
			text += `用件があると確かめられたら、通常のタスクに記録できなかった場合も matters に書きます。
`
		case j.Output == "matters":
			text += task.OwnerName + `にお願いすることが残ったら、matters に1件ずつ書きます。用件があると確かめられたら、通常のタスクに記録できなかった場合も含めます。matters の各項目には key と revision を入れ、お願いすることを summary に具体的に書きます。同じ用件の同じ更新には同じ key と revision を使います。用件をどこへ保存して知らせるかが本文や手順で決まっていれば、その指定に従います。
`
		}
		if len(j.CheckScopes) == 0 {
			text += `確認する範囲が1つだけで、その範囲をすべて確認できた回は、{"version":1,"check_status":"complete","summary":"確認しました。新しい用件は0件です。"} と書けば、サーバーが定期タスク全体を checks に補います。
`
		}
	} else if j.CheckResult {
		text += `結果は JSON で書き、version には 1 を指定します。check_status は、予定したすべての範囲を確認できたら complete、一部だけなら partial、どの範囲も確認できなければ unavailable にします。
checks には、手順で予定した範囲をすべて含めます。各範囲を scope に書き、status には確認できたら verified、できなければ unverified を指定します。読めなかった理由は reason に添えます。同じ原因だと確かめたときだけ同じ cause_key を使います。scope や理由に秘密の値やコマンドの全出力は書きません。
新着がなければ nothing: true を、新着があれば matters と summary を保存します。新着が0件だと言えるのは、予定した範囲をすべて確認できたときです。全範囲を確認して新着がなかった場合の例は {"version":1,"check_status":"complete","nothing":true,"checks":[{"scope":"service/status","status":"verified"}]} です。接続できない例は {"version":1,"check_status":"unavailable","nothing":true,"summary":"接続できず確認できませんでした。","checks":[{"scope":"service/status","status":"unverified","reason":"connection_failed"}]} です。
`
	}
	if len(j.CheckScopes) > 0 {
		text += "この回で確かめる範囲は、受付のときに決めてあります。checks の scope には次の範囲だけを1回ずつ書いてください。確認できなかった範囲も含め、unverified と理由を添えます。対象は " + strings.Join(j.CheckScopes, "、") + " です。\n"
	}
	return text
}

// TaskInstruction tells the conversation that runs a routine about the
// routine's task (task.md in its directory), when it has one: read it first,
// keep each run's findings in the receipts as before, and rewrite the task
// only when what should be done has changed. How to tell the owner stays as
// the routine's own text says. With collected set, the server itself gathers
// the run's matters into the task, so the text says what is left to write by
// hand.
func (j Job) TaskInstruction(root string, collected bool) string {
	if j.ID == "" || j.Path == "" {
		return ""
	}
	dir := path.Dir(filepath.ToSlash(j.Path))
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), task.TaskFile)); err != nil {
		return ""
	}
	body, rewrite := "結果を受付に保存しても、タスクの本文は自動では変わりません。", "次にすることが変わったときだけ"
	if collected {
		body, rewrite = "実行結果の matters に書いた用件は、サーバーが本文の「実行から分かった対応」へ集めます。", "それ以外に、次にすることが変わったときだけ"
	}
	return fmt.Sprintf("\n\nこの定期タスクの本文（%s/task.md）には、次にすることが書いてあります。始める前に `lacia task show %s` で本文を読み、書かれていることを踏まえて進めてください。"+
		"この回の分析結果は受付に保存します。%s"+
		"%s、`lacia rules tasks` の「定期タスクのタスク」に従って `lacia task write %s task.md <ファイル>` で本文を書き直し、どの回の結果をもとに直したかを `lacia task note %s` で履歴に残します。"+
		"状態は進行中、待機、確認待ちのどれかです。%sへの知らせ方は、この定期タスクの「伝え方」に従います。\n", dir, j.ID, body, rewrite, j.ID, j.ID, task.OwnerName)
}

func (j Job) onDay(day time.Time) bool {
	return j.WeekdayMask == 0 || (j.WeekdayMask&(1<<day.Weekday())) != 0
}

func withEnabled(j Job) Job {
	j.Enabled = true
	return j
}

// Next is the first enabled occurrence strictly after now.
func (j Job) Next(now time.Time) time.Time {
	if !j.Enabled || j.Err != "" {
		return time.Time{}
	}
	now = now.In(j.zone())
	if j.Every > 0 {
		next := now
		for range 8 * 24 * time.Hour / j.Every {
			day := time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, j.zone())
			candidate := day.Add((next.Sub(day)/j.Every + 1) * j.Every)
			if tomorrow := day.AddDate(0, 0, 1); !candidate.Before(tomorrow) {
				candidate = tomorrow
			}
			if at, ok := j.due(candidate, candidate.Add(-time.Second), time.Second); ok && at.Equal(candidate) {
				return at
			}
			next = candidate
		}
		return time.Time{}
	}
	for delta := range 8 {
		day := now.AddDate(0, 0, delta)
		for _, clock := range strings.Split(j.Daily, ", ") {
			at, _ := time.ParseInLocation(slotLayout, day.Format(time.DateOnly)+"T"+clock, j.zone())
			if !at.After(now) {
				continue
			}
			if due, ok := j.due(at, at.Add(-time.Second), time.Second); ok && due.Equal(at) {
				return at
			}
		}
	}
	return time.Time{}
}
