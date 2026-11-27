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
	text := fmt.Sprintf("\n\nこの回の定期タスクIDは %s、実行IDは %s です。結果を lacia run report %s %s <JSONファイル> で保存してください。\n", id, runID, id, runID)
	if j.ExplicitMatters {
		text += `結果はJSONで書きます。予定した範囲を確認できたら check_status を complete にし、確認結果や件数は summary に書きます。要約だけなら実行履歴に残ります。
`
		if collected {
			text += `本人にお願いする用件や、実行のあとも残る用件は、matters の各項目に key、revision、具体的にしていただきたいこと（summary）を書きます。同じ用件の同じ更新には同じ key と revision を使います。サーバーが、matters の用件を定期タスクのタスクへ集めます。通常のタスクは新しく作りません。
handoffs を使うのは、同じ用件をすでに追っている通常のタスクがあるときと、定義の「すること」が通常のタスクを作ると名前を挙げて書いているときだけです。そのときは、通常のタスクへの記録を読み戻してから、handoffs の各項目に key、revision、summary、task_id を書きます。
`
		} else {
			text += `通常のタスクで用件を知らせる手順の場合は、そこへの記録を読み戻してから、handoffs の各項目に key、revision、summary、task_id を書きます。
`
		}
		text += `一部または全部を確認できなければ check_status を partial または unavailable にし、checks に scope と status（verified または unverified）を入れます。未確認には reason を添え、同じ原因だと確かめたときだけ cause_key をそろえます。通常のタスクに記録できなかったことも、確認できなかった範囲と理由に残します。全範囲を読み取れず、別に認証などの操作が必要だと確かめたときは、手順で決まっている方法でその操作を案内します。秘密の値やコマンドの全出力は書きません。返信先へのお知らせは定期タスクの本文に従います。
`
		switch {
		case collected:
			text += `通常のタスクへの記録に失敗した確定用件も、matters に書きます。
`
		case j.Output == "matters":
			text += `本人にお願いする用件が残った場合は、通常のタスクへの記録に失敗した確定用件も含め、matters の各項目に key、revision、具体的にしていただきたいこと（summary）を書きます。同じ用件の同じ更新には同じ key と revision を使います。用件をどこへ保存して知らせるかが本文や手順で決まっていれば、その指定に従います。
`
		}
		if len(j.CheckScopes) == 0 {
			text += `確認範囲が一つで全部を確認できた回は、{"version":1,"check_status":"complete","summary":"確認しました。新しい用件は0件です。"} と書けば、サーバーが定期タスク全体を確認した範囲として補います。
`
		}
	} else if j.CheckResult {
		text += `結果は version: 1、check_status: complete（予定した全範囲を確認）、partial（一部を確認できない）、unavailable（全範囲を確認できない）を持つJSONです。checksには予定した各範囲のscopeとstatus（verifiedかunverified）を入れ、読めなかった範囲にはreasonを添えます。同じ原因と確かめたときだけcause_keyをそろえます。確認する範囲は、手順で予定した範囲を保ってください。新着がなければnothing: true、新着があればmattersとsummaryを保存します。確認できなかったことを正常な0件にしません。scopeと理由には秘密の値やコマンドの全出力を入れません。正常な0件の例は {"version":1,"check_status":"complete","nothing":true,"checks":[{"scope":"service/status","status":"verified"}]} です。接続できない例は {"version":1,"check_status":"unavailable","nothing":true,"summary":"接続できず確認できませんでした。","checks":[{"scope":"service/status","status":"unverified","reason":"connection_failed"}]} です。
`
	}
	if len(j.CheckScopes) > 0 {
		text += "この回の確認範囲は受付時に固定しています。checksには、次のscopeを過不足なく1回ずつ入れてください。確認できない範囲も削らず、unverifiedと理由を残します。範囲: " + strings.Join(j.CheckScopes, ", ") + "。\n"
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
	body, rewrite := "タスクの本文は、受付からは自動で変わりません。", "すべきことが変わったときだけ"
	if collected {
		body, rewrite = "実行結果の matters に書いた用件は、サーバーが本文の「実行から分かった対応」へ集めます。", "それ以外のすべきことが変わったときだけ"
	}
	return fmt.Sprintf("\n\nこの定期タスクには、いま何をすべきかを書いたタスクの本文があります（%s/task.md）。始める前に `lacia task show %s` で読み、書かれていることを踏まえて進めてください。"+
		"この回の分析の結果は、これまでどおり受付に保存します。%s"+
		"%s、`lacia rules tasks` の「定期タスクのタスク」に従って `lacia task write %s task.md <ファイル>` で本文を書き直し、どの受付を受けて直したのかを `lacia task note %s` で履歴に残します。"+
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
