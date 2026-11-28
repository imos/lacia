package task

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClosingIsAStatusOfItsOwn(t *testing.T) {
	if !Closing.Valid() || Closing.Closed() || Closing.Label() != "完了確認待ち" || Review.Label() != "確認待ち" {
		t.Fatalf("closing: valid %v, closed %v, label %q", Closing.Valid(), Closing.Closed(), Closing.Label())
	}
	for status, want := range map[Status]bool{Review: true, Closing: true, Todo: false, Doing: false, Waiting: false, Done: false, Cancelled: false} {
		if status.AwaitsUser() != want {
			t.Errorf("%s: AwaitsUser = %v, want %v", status, status.AwaitsUser(), want)
		}
	}
	if len(Statuses) != 7 || Statuses[0] != Review || Statuses[1] != Closing {
		t.Errorf("statuses in display order: %v", Statuses)
	}

	s := newStore(t)
	a, _ := s.Create(NewTask{Area: "work", Title: "終わったタスク", Status: Doing})
	if got, changed, err := s.SetStatus(a.ID, Closing, "テスト", ""); err != nil || !changed || got.Status != Closing {
		t.Fatalf("to closing: %+v, %v, %v", got, changed, err)
	}
	if err := s.Reload(); err != nil {
		t.Fatalf("a closing task did not load: %v", err)
	}
	got, err := s.Get(a.ID)
	if err != nil || got.Status != Closing || got.Done != "" {
		t.Fatalf("after reload: %+v, %v", got, err)
	}
	log, _ := readTestHistory(t, filepath.Join(s.Root(), got.Dir))
	if !strings.Contains(string(log), "状態を「進行中」から「完了確認待ち」に変えました。") {
		t.Errorf("log:\n%s", log)
	}
	if got, _, err := s.SetStatus(a.ID, Done, "Web", ""); err != nil || got.Status != Done || got.Done == "" {
		t.Errorf("to done: %+v, %v", got, err)
	}
	if _, _, err := s.SetStatus(a.ID, Status("confirm"), "テスト", ""); err == nil || !strings.Contains(err.Error(), "closing") {
		t.Errorf("an unknown status: %v", err)
	}
}

func TestClosingStaysWithTheUserWhileOtherTasksAreOpen(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, JST)
	for _, status := range []Status{Review, Closing} {
		waiting := Task{Status: status, Blockers: []string{"T1001"}}
		if waiting.Snoozed(now) || waiting.ListStatus(now, false, false) != status {
			t.Errorf("%s with an open dependency left the user's list", status)
		}
		if later := (Task{Status: status, ResumeOn: "2026-11-27"}); !later.Snoozed(now) {
			t.Errorf("%s with a later resume_on was not set aside", status)
		}
	}
	if blocked := (Task{Status: Todo, Blockers: []string{"T1001"}}); !blocked.Snoozed(now) {
		t.Error("an unstarted task with an open dependency must still wait")
	}
}

// The server never puts a task into "closing". When something new comes up
// for a task that is there, the user has to read it again: it goes to
// "review" like any other open task.
func TestTheServerMovesAClosingTaskToReview(t *testing.T) {
	s := newStore(t)
	closing := func(title string) Task {
		created, _ := s.Create(NewTask{Area: "work", Title: title, Status: Doing})
		return created
	}

	timed := closing("日時のあるタスク")
	s.SetField(timed.ID, "review_at", "2026-11-19", "")
	s.SetStatus(timed.ID, Closing, "", "")
	if woken := s.Wake(time.Date(2026, 11, 19, 0, 0, 0, 0, JST)); len(woken) != 1 {
		t.Fatalf("woken = %v", woken)
	}
	if got, _ := s.Get(timed.ID); got.Status != Review || got.ReadySince == "" || got.ReviewAt != "" {
		t.Errorf("after the time came: %+v", got)
	}

	waited := closing("待ちの結果が出たタスク")
	s.SetStatus(waited.ID, Closing, "", "")
	if _, changed, err := s.Ready(waited.ID, "待っていた処理が終わりました。", false); err != nil || !changed {
		t.Fatalf("ready: %v, %v", changed, err)
	}
	if got, _ := s.Get(waited.ID); got.Status != Review || got.ReadySince == "" {
		t.Errorf("after a result: %+v", got)
	}

	looped := closing("書き込みが続いたタスク")
	s.SetStatus(looped.ID, Closing, "", "")
	if _, changed, err := s.Stop(looped.ID, "同じ書き込みが続きました。"); err != nil || !changed {
		t.Fatalf("stop: %v, %v", changed, err)
	}
	if got, _ := s.Get(looped.ID); got.Status != Review || got.StoppedAt == "" {
		t.Errorf("after a stop: %+v", got)
	}
}

func TestCheckTellsTheTwoWaysOfAskingApart(t *testing.T) {
	body := func(next string) string {
		return "## ひとことで\n\n表示を直しました。\n\n## 次にすること\n\n" + next + "\n"
	}
	tests := []struct {
		name   string
		status Status
		next   string
		want   string
	}{
		{"review that only asks to confirm", Review, OwnerName + "に、結果をご確認いただきます。", "確認のお願いだけで終わっています"},
		{"review that only asks to confirm, with a link", Review, OwnerName + "に、[結果](artifacts/result.md)をご確認ください。\n\n検証は済んでいます。", "`closing`（完了確認待ち）"},
		{"review that names the decision in the same paragraph", Review, OwnerName + "に、結果をご確認いただきます。公開してよいかをお知らせください。", ""},
		{"review that asks to decide", Review, OwnerName + "に、公開するかを決めていただきます。", ""},
		{"review that asks to look", Review, OwnerName + "に、表示を見ていただきます。", ""},
		{"review without the reviewer", Review, "結果を見て、公開するかを決めます。", "確認する方の名前"},
		{"closing that tells what is finished", Closing, "表示を直して、本番に反映しました。結果のページで見ていただけます。", ""},
		{"closing that asks to confirm", Closing, OwnerName + "に、反映した結果をご確認いただきます。", ""},
		{"closing that asks to decide", Closing, "反映しました。公開の範囲を決めていただきます。", "「決めて」"},
		{"closing that asks for a judgement", Closing, "反映しました。公開してよいか、ご判断ください。", "`review`（確認待ち）"},
		{"closing that invites a request in a later paragraph", Closing, "反映まで済んでいます。\n\n直したいところがあれば、ご指示ください。", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings := Check(Task{ID: "T1001", Title: "表示を直す", Status: test.status, Body: body(test.next)}, nil)
			var messages []string
			for _, w := range warnings {
				messages = append(messages, w.Message)
			}
			joined := strings.Join(messages, "\n")
			if test.want == "" && len(warnings) != 0 {
				t.Errorf("unexpected advice:\n%s", joined)
			}
			if test.want != "" && (len(warnings) != 1 || !strings.Contains(joined, test.want)) {
				t.Errorf("advice lacks %q:\n%s", test.want, joined)
			}
		})
	}
}

func TestStateWarningsCoverClosing(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, JST)
	got := StateWarnings(Task{Status: Closing, ResumeOn: "2026-11-26"}, nil, StateContext{Now: now})
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "完了確認待ちですが、resume_on は先の日時（2026-11-26）です。") {
		t.Errorf("closing with a later resume_on: %+v", got)
	}
	if got := StateWarnings(Task{Status: Closing, ResumeOn: "2026-11-24"}, nil, StateContext{Now: now}); len(got) != 0 {
		t.Errorf("closing with a past resume_on: %+v", got)
	}
}
