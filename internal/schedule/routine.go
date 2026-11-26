package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/imos/lacia/internal/routine"
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
func (j Job) ReportInstruction(runID string) string {
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
	text := fmt.Sprintf("\n\nこの回の定期タスクIDは %s、実行IDは %s です。結果を lacia run report %s %s <JSONファイル> で保存してください。\n", id, runID, id, runID)
	if j.ExplicitMatters {
		text += `結果はJSONで書きます。予定した範囲を確認できたら check_status を complete にし、確認結果や件数は summary に書きます。要約だけなら実行履歴に残ります。
通常のタスクで用件を知らせる手順の場合は、そこへの記録を読み戻してから、handoffs の各項目に key、revision、summary、task_id を書きます。
一部または全部を確認できなければ check_status を partial または unavailable にし、checks に scope と status（verified または unverified）を入れます。未確認には reason を添え、同じ原因だと確かめたときだけ cause_key をそろえます。通常のタスクに記録できなかったことも、確認できなかった範囲と理由に残します。全範囲を読み取れず、別に認証などの操作が必要だと確かめたときは、手順で決まっている方法でその操作を案内します。秘密の値やコマンドの全出力は書きません。返信先へのお知らせは定期タスクの本文に従います。
`
		if j.Output == "matters" {
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
