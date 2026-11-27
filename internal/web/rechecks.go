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
	return fmt.Sprintf(`外部待ちのタスク %[1]s の、決めてあった確認の回（%[2]s）です。このタスクは、外部の変化を繰り返し確かめながら待っています。確認は%[3]sで、期限は %[4]s です。次の回の日時は、サーバーがすでに入れてあります。この回でしていただくのは、読み取りの確認と、結果の記録だけです。

1. lacia task show %[1]s で本文を読み、補足（notes.md）があれば読みます。「次にすること」に、何を確かめるのかと、何が起きたら%[5]sにお知らせするのかが書いてあります。
2. 書いてある対象を、読み取りだけで確かめます。外部への送信、投稿、書き込み、設定の変更はしません。タスクの状態と条件は、下のコマンドのほかでは変えません。
3. 結果を、次のどれか1つで残します。文章には、何を見て、どうだったのかを1〜2文で書きます。
   - 変化がなかったとき: lacia task rechecked %[1]s same "（見たものと結果）"
   - %[5]sの判断や操作が要る変化があったとき: 先に lacia task write %[1]s task.md で「次にすること」へ、見ていただくものへのリンクと、決めていただきたいことを書きます。そのあと lacia task rechecked %[1]s changed "（何が変わったか）" を実行します。タスクは確認待ちになり、繰り返しの確認は止まります。
   - 対象を読めなかったとき（認証切れ、通信の失敗など）: lacia task rechecked %[1]s unknown "（確かめられなかった範囲と理由）"。読めなかった回を、変化なしとは扱いません。
4. 次の回を別の日時にしたほうがよいと分かったときだけ、lacia task field %[1]s resume_on "日時" で変えます。たとえば、相手が返事をする日を書いていたときです。期限より後にはできません。

結果を残さずに終えると、その回は「確かめられなかった」と数えます。%[6]d回続くと、サーバーがタスクを確認待ちへ出します。返事では、残した結果を1文で伝えてください。`,
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
	http.Redirect(w, r, "/tasks/"+t.ID+"?flash="+url.QueryEscape(t.RecheckLabel()+"。最初の確認は "+t.ResumeOn+" です。"), http.StatusSeeOther)
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
