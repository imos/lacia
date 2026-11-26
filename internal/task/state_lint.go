package task

import (
	"fmt"
	"time"
)

// StateContext holds known command-use times, not process liveness. A nil
// OwnerLast skips that check; it never infers activity from task history.
type StateContext struct {
	Now        time.Time
	OwnerLast  map[string]time.Time
	StaleAfter time.Duration
}

// StateWarnings advises about mismatched state and conditions without changing
// tasks. tasks must contain the repository, even when only one task is checked.
func StateWarnings(t Task, tasks map[string]Task, context StateContext) []Warning {
	if t.Status.Closed() {
		return nil
	}
	var out []Warning
	if t.Status.AwaitsUser() && t.ResumeOn != "" && t.Snoozed(context.Now) {
		out = append(out, Warning{Message: fmt.Sprintf("%sですが、resume_on は先の日時（%s）です。いま確認していただくのか、その日時まで待つのかを確かめてください。", t.Status.Label(), t.ResumeOn)})
	}
	if t.AfterRun != "" {
		if _, ok := runRegisteredAt(t.AfterRunSince); !ok {
			out = append(out, Warning{Message: fmt.Sprintf("定期タスク %s を待ちますが、待機の登録時刻がありません。lacia task field %s after_run %s で待機を登録し直してください。", t.AfterRun, t.ID, t.AfterRun)})
		}
	}
	if t.Status == Doing && t.OwnerSession != "" && context.StaleAfter > 0 {
		if last, ok := context.OwnerLast[t.OwnerSession]; ok && !last.IsZero() && context.Now.Sub(last) >= context.StaleAfter {
			out = append(out, Warning{Message: fmt.Sprintf("進行中ですが、担当の会話 %s が最後にコマンドを使った記録は %s で、%d時間以上前です。会話がいまも動いているかは、この記録だけでは分かりません。", t.Owner, last.Format(time.RFC3339), int(context.StaleAfter.Hours()))})
		}
	}
	for _, id := range t.After {
		dependency, ok := tasks[id]
		if ok && dependency.Status == Todo && dependency.ResumeOn == "" && dependency.ReviewAt == "" && dependency.AfterRun == "" && dependency.OnReady == "" {
			out = append(out, Warning{Message: fmt.Sprintf("after で待つタスク %s（%s）は未着手で、日時（resume_on、review_at）も定期タスクの結果待ち（after_run）も自動で着手する指定（on_ready）もありません。どのように着手するかを確かめてください。", dependency.ID, dependency.Title)})
		}
	}
	return out
}
