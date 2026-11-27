package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

// Recheck moves on the waiting tasks whose next look became due and asks a
// conversation to look at each. The store has written the round after this
// one already, so a look that never reports cannot end the rounds. It
// returns whether a task changed.
func (s *Server) Recheck(now time.Time) bool {
	rounds := s.Store.Recheck(now, s.recheckPending)
	for _, round := range rounds {
		if round.Asked == "" {
			continue // went to review
		}
		if err := s.askRecheck(round); err != nil {
			slog.Error("ask to recheck", "task", round.Task.ID, "error", err)
			if _, _, err := s.Store.RecheckNotAsked(round.Task.ID, err.Error()); err != nil {
				slog.Error("recheck not asked", "task", round.Task.ID, "error", err)
			}
		}
	}
	return len(rounds) > 0
}

// recheckRequestID names the look of one round, so that a server that
// stopped between writing the round and queuing the look asks only once.
func recheckRequestID(id, asked string) string {
	return "recheck-" + id + "-" + strings.NewReplacer("-", "", ":", "", " ", "-").Replace(asked)
}

// recheckPending reports a look that is still in the queue or running.
func (s *Server) recheckPending(id, asked string) bool {
	if s.Requests == nil {
		return false
	}
	r, ok := s.Requests.Get(recheckRequestID(id, asked))
	return ok && (r.Status == request.Queued || r.Status == request.Running)
}

// askRecheck queues the look in the side conversation, like a routine: it
// must not wait behind the requests of the main one, and it must not make
// that conversation longer.
func (s *Server) askRecheck(round task.RecheckRound) error {
	if s.Requests == nil {
		return errors.New("このサーバーは、会話への依頼を受け付けていません")
	}
	t := round.Task
	_, _, err := s.Requests.Add(request.Request{ID: recheckRequestID(t.ID, round.Asked), TaskID: t.ID, Kind: request.System,
		Source: "外部待ちの確認", Side: true, Text: recheckPrompt(t, round.Asked)})
	return err
}

// recheckPrompt is all the conversation that looks is told. It knows
// nothing of how the task came to wait.
func recheckPrompt(t task.Task, asked string) string {
	id, owner := t.ID, task.OwnerName
	return fmt.Sprintf(`タスク %[1]s で、外部の変化を確かめる時刻（%[2]s）になりました。確認は%[3]sで、期限は %[4]s です。次の確認日時はサーバーが登録しています。今回は、対象を読み取って結果を記録してください。

1. lacia task show %[1]s で本文を読んでください。補足（notes.md）があれば、そちらも読みます。「次にすること」に、何を確かめて、どんな変化を%[5]sにお知らせするかが書いてあります。
2. 指定された対象は、読み取りだけで確かめてください。外部への送信、投稿、書き込み、設定の変更はしません。タスクの状態や条件を変えるときは、次に挙げるコマンドだけを使います。
3. 何を確かめてどうだったかを1〜2文で書き、次のうち1つの方法で記録してください。
   - 変化がなければ、lacia task rechecked %[1]s same "見たものと結果" を実行します。
   - %[5]sの判断や操作が要る変化があれば、先にタスクの「次にすること」に、見ていただくものへのリンクと、お願いしたいことを書いてください。書き直した本文を lacia task write %[1]s task.md ファイル名 で保存してから、lacia task rechecked %[1]s changed "変わったこと" を実行します。タスクは「確認待ち」になり、繰り返しの確認は終わります。
   - 認証切れや通信の失敗などで対象を読めなかったときは、lacia task rechecked %[1]s unknown "確かめられなかった範囲と理由" を実行します。
4. 相手が返事をする日が分かったときなど、次の確認日時を変えたほうがよい場合だけ、lacia task field %[1]s resume_on "日時" で変更してください。指定できるのは期限までの日時です。

結果を記録しなかった回も、確かめられなかった回に数えます。%[6]d回続くと、サーバーがタスクを「確認待ち」にします。返事では、記録した結果を1文で伝えてください。`,
		id, asked, task.EveryLabel(t.RecheckEvery), t.RecheckUntil, owner, task.MaxRecheckMisses)
}

// setRecheck registers, from the task's page, how often and until when a
// waiting task is looked at.
func (s *Server) setRecheck(w http.ResponseWriter, r *http.Request) {
	next := strings.TrimSpace(r.FormValue("date") + " " + r.FormValue("time"))
	until := strings.TrimSpace(r.FormValue("until") + " " + r.FormValue("until_time"))
	t, _, err := s.Store.SetRecheck(r.PathValue("id"), r.FormValue("every"), until, next, "Web")
	switch {
	case errors.Is(err, task.ErrNotFound):
		s.fail(w, http.StatusNotFound, "タスクが見つかりませんでした。")
		return
	case err != nil:
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.changed()
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(task.EveryLabel(t.RecheckEvery)+"の確認を "+t.RecheckUntil+" まで続けます。最初の確認は "+t.ResumeOn+" です。"), http.StatusSeeOther)
}

// recheckChoice is one interval offered on the task's page. The command
// takes any number of hours or days.
type recheckChoice struct{ Value, Label string }

func recheckChoices() []recheckChoice {
	var out []recheckChoice
	for _, value := range []string{"1d", "12h", "6h", "3h", "7d"} {
		out = append(out, recheckChoice{value, task.EveryLabel(value)})
	}
	return out
}
