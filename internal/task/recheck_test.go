package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recheckStore has one task that is being worked on, at 10:30 on
// 2026-11-12 in Japan.
func recheckStore(t *testing.T) (*Store, Task, func(time.Time)) {
	t.Helper()
	s := newStore(t)
	item, err := s.Create(NewTask{Area: "work", Title: "審査の返事を待つ", Status: Doing})
	if err != nil {
		t.Fatal(err)
	}
	return s, item, func(now time.Time) { s.now = func() time.Time { return now } }
}

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, JST)
}

func mustRecheck(t *testing.T, s *Store, id, every, until, next string) Task {
	t.Helper()
	got, changed, err := s.SetRecheck(id, every, until, next, "テスト")
	if err != nil || !changed {
		t.Fatalf("register: changed=%v err=%v", changed, err)
	}
	return got
}

func historyOf(t *testing.T, s *Store, item Task) string {
	t.Helper()
	data, err := readTestHistory(t, filepath.Join(s.Root(), item.Dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseEvery(t *testing.T) {
	for value, want := range map[string]time.Duration{"1h": time.Hour, "3h": 3 * time.Hour, "1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		if got, err := ParseEvery(value); err != nil || got != want {
			t.Errorf("ParseEvery(%q) = %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "0h", "30m", "1", "d", "1w", "-1d", "1.5h", "毎日"} {
		if _, err := ParseEvery(value); err == nil {
			t.Errorf("ParseEvery(%q) was accepted", value)
		}
	}
	for value, want := range map[string]string{"1d": "毎日", "2d": "2日ごと", "3h": "3時間ごと"} {
		if got := EveryLabel(value); got != want {
			t.Errorf("EveryLabel(%q) = %q", value, got)
		}
	}
}

func TestRegisteringARecheckLeavesTheTaskWaiting(t *testing.T) {
	s, item, _ := recheckStore(t)
	other, _ := s.Create(NewTask{Area: "work", Title: "別のタスク"})
	for name, args := range map[string][3]string{
		"no interval":                    {"", "2026-12-01", ""},
		"interval under an hour":         {"30m", "2026-12-01", ""},
		"no deadline":                    {"1d", "", ""},
		"deadline in the past":           {"1d", "2026-11-12 10:00", ""},
		"first round in the past":        {"1d", "2026-12-01", "2026-11-12 09:00"},
		"first round after the deadline": {"1d", "2026-11-20", "2026-11-21 09:00"},
		"no round before the deadline":   {"7d", "2026-11-15", ""},
	} {
		if _, changed, err := s.SetRecheck(item.ID, args[0], args[1], args[2], ""); err == nil || changed {
			t.Errorf("%s was accepted", name)
		}
	}
	if got, _ := s.Get(item.ID); got.Status != Doing || got.Rechecks() {
		t.Fatalf("a refused registration changed the task: %+v", got)
	}
	// The rounds are the one thing that brings the task back.
	for _, c := range []Field{{"after", other.ID}, {"after_run", "S1001"}, {"review_at", "2026-11-20"}, {"on_ready", "start"}} {
		if _, _, err := s.SetField(item.ID, c.Key, c.Value, ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetRecheck(item.ID, "1d", "2026-12-01", "", ""); err == nil || !strings.Contains(err.Error(), "lacia task field "+item.ID+" "+c.Key) {
			t.Errorf("registered together with %s: %v", c.Key, err)
		}
		if _, _, err := s.SetField(item.ID, c.Key, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Claim(item.ID, Owner{Label: "local-claude", Session: "k"}, "", ""); err != nil {
		t.Fatal(err)
	}

	got := mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
	if got.Status != Waiting || got.Owner != "" || got.RecheckEvery != "1d" || got.RecheckUntil != "2027-01-15" || got.ResumeOn != "2026-11-13 09:00" || !got.HasReadyConditions() {
		t.Fatalf("registered: %+v", got)
	}
	if label, state := got.RecheckLabel(), got.RecheckState(); label != "毎日 09:00 に確認（2027-01-15 まで）" || state != "初回の確認を待っています。次回は 11/13 09:00" {
		t.Fatalf("label %q, state %q", label, state)
	}
	if got.ReadyLabel(false).String() != got.RecheckLabel() || got.MissingWaitingCondition(false) {
		t.Fatalf("the lists do not show the rounds: %q", got.ReadyLabel(false).String())
	}
	if log := historyOf(t, s, got); !strings.Contains(log, "毎日に確かめながら待ちます") || !strings.Contains(log, "状態を「進行中」から「外部待ち」に変えました") {
		t.Fatalf("history: %s", log)
	}
	if raw, err := os.ReadFile(filepath.Join(s.Root(), got.Dir, HistoryFile)); err != nil || !strings.Contains(string(raw), `"type":"recheck"`) || !strings.Contains(string(raw), `"action":"registered"`) {
		t.Fatalf("the event has no kind of its own: %s %v", raw, err)
	}
	// The same registration again changes nothing.
	if _, changed, err := s.SetRecheck(item.ID, "1d", "2027-01-15", "2026-11-13 09:00", ""); err != nil || changed {
		t.Fatalf("repeated registration: changed=%v err=%v", changed, err)
	}
	// Without a first round, it comes one interval from now.
	got = mustRecheck(t, s, item.ID, "3h", "2027-01-15 18:00", "")
	if got.ResumeOn != "2026-11-12 13:30" || got.RecheckLabel() != "3時間ごとに確認（2027-01-15 18:00 まで）" {
		t.Fatalf("default first round: %+v", got)
	}

	closed, _ := s.Create(NewTask{Area: "work", Title: "終わったタスク", Status: Done})
	if _, _, err := s.SetRecheck(closed.ID, "1d", "2026-12-01", "", ""); err == nil {
		t.Fatal("registered on a closed task")
	}
}

func TestTheServerWritesTheNextRoundBeforeItAsks(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")

	if rounds := s.Recheck(at(13, 8, 59), nil); len(rounds) != 0 {
		t.Fatalf("asked before the round: %+v", rounds)
	}
	// The ordinary wake leaves the date of the next round alone.
	if woken := s.Wake(at(13, 9, 0)); len(woken) != 0 {
		t.Fatalf("the round woke the task: %+v", woken)
	}
	setNow(at(13, 9, 0))
	rounds := s.Recheck(at(13, 9, 0), nil)
	if len(rounds) != 1 || rounds[0].Asked != "2026-11-13 09:00" {
		t.Fatalf("round: %+v", rounds)
	}
	got := rounds[0].Task
	if got.Status != Waiting || got.ResumeOn != "2026-11-14 09:00" || got.RecheckAsked != "2026-11-13 09:00" || got.RecheckMissed != 0 {
		t.Fatalf("after asking: %+v", got)
	}
	if state := got.RecheckState(); state != "11/13 09:00 の回の結果を待っています。次回は 11/14 09:00" {
		t.Fatalf("state while asked: %q", state)
	}
	// Asked once for one round.
	if rounds := s.Recheck(at(13, 9, 1), nil); len(rounds) != 0 {
		t.Fatalf("asked twice: %+v", rounds)
	}

	setNow(at(13, 9, 2))
	got, changed, err := s.Rechecked(item.ID, RecheckSame, "Issue を最後のページまで読み、新しいコメントはありませんでした。", "side")
	if err != nil || !changed || got.Status != Waiting || got.RecheckAsked != "" || got.RecheckLast != "2026-11-13 09:02" || got.ResumeOn != "2026-11-14 09:00" {
		t.Fatalf("same: %+v changed=%v err=%v", got, changed, err)
	}
	if state := got.RecheckState(); state != "最終確認 11/13 09:02、変化なし。次回は 11/14 09:00" {
		t.Fatalf("state after a look: %q", state)
	}
	if warnings := StateWarnings(got, nil, StateContext{Now: at(13, 10, 0)}); len(warnings) != 0 {
		t.Fatalf("warnings for a healthy task: %v", warnings)
	}
	log := historyOf(t, s, got)
	for _, want := range []string{"決めてあった確認の回（2026-11-13 09:00）になりました", "次の回は 2026-11-14 09:00 です", "確認の結果は「変化なし」です。Issue を最後のページまで読み"} {
		if !strings.Contains(log, want) {
			t.Errorf("history lacks %q:\n%s", want, log)
		}
	}
}

func TestRoundsMissedWhileTheServerWasDownBecomeOne(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
	// Three rounds passed.
	setNow(at(15, 14, 0))
	rounds := s.Recheck(at(15, 14, 0), nil)
	if len(rounds) != 1 || rounds[0].Asked != "2026-11-13 09:00" || rounds[0].Task.ResumeOn != "2026-11-16 09:00" {
		t.Fatalf("after the gap: %+v", rounds)
	}
	if rounds := s.Recheck(at(15, 14, 1), nil); len(rounds) != 0 {
		t.Fatalf("the rounds of the gap were asked one by one: %+v", rounds)
	}
}

func TestTwoRoundsWithoutAWorkingLookGoToReview(t *testing.T) {
	for name, second := range map[string]string{"no result twice": "", "could not look, then no result": RecheckUnknown} {
		t.Run(name, func(t *testing.T) {
			s, item, setNow := recheckStore(t)
			mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
			setNow(at(13, 9, 0))
			if rounds := s.Recheck(at(13, 9, 0), nil); len(rounds) != 1 {
				t.Fatal("first round was not asked")
			}
			if second == RecheckUnknown {
				got, _, err := s.Rechecked(item.ID, RecheckUnknown, "認証が切れていて、Issue を読めませんでした。", "side")
				if err != nil || got.Status != Waiting || got.RecheckMissed != 1 || got.RecheckAsked != "" {
					t.Fatalf("unknown: %+v %v", got, err)
				}
				if state := got.RecheckState(); !strings.HasPrefix(state, "前回は確かめられませんでした") {
					t.Fatalf("state after a look that failed: %q", state)
				}
				if warnings := StateWarnings(got, nil, StateContext{Now: at(13, 10, 0)}); len(warnings) != 1 || !strings.Contains(warnings[0].Message, "確かめられませんでした") {
					t.Fatalf("lint after a look that failed: %v", warnings)
				}
			}
			// The second round is still asked: one miss can be chance.
			setNow(at(14, 9, 0))
			rounds := s.Recheck(at(14, 9, 0), nil)
			if len(rounds) != 1 || rounds[0].Asked != "2026-11-14 09:00" || rounds[0].Task.RecheckMissed != 1 || rounds[0].Task.Status != Waiting {
				t.Fatalf("second round: %+v", rounds)
			}
			if second == "" && !strings.Contains(historyOf(t, s, rounds[0].Task), "前の回（2026-11-13 09:00）は、結果が残りませんでした") {
				t.Fatal("the history does not say that the round before left no result")
			}
			// The second round ends without a result too.
			setNow(at(15, 9, 0))
			rounds = s.Recheck(at(15, 9, 0), nil)
			if len(rounds) != 1 || rounds[0].Asked != "" {
				t.Fatalf("third round was asked: %+v", rounds)
			}
			got := rounds[0].Task
			if got.Status != Review || got.Rechecks() || got.ResumeOn != "" || got.RecheckMissed != 0 || got.HasReadyConditions() {
				t.Fatalf("after two misses: %+v", got)
			}
			if log := historyOf(t, s, got); !strings.Contains(log, "確認が2回続けて") || !strings.Contains(log, "状態を「外部待ち」から「確認待ち」に変えました") {
				t.Fatalf("history: %s", log)
			}
		})
	}
}

func TestALookThatWorkedForgetsTheMissBefore(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
	for day := 13; day <= 16; day++ {
		setNow(at(day, 9, 0))
		rounds := s.Recheck(at(day, 9, 0), nil)
		if len(rounds) != 1 || rounds[0].Asked == "" {
			t.Fatalf("day %d: %+v", day, rounds)
		}
		// Every other round leaves no result.
		if day%2 == 0 {
			if _, _, err := s.Rechecked(item.ID, RecheckSame, "変化はありませんでした。", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, _ := s.Get(item.ID); got.Status != Waiting || got.RecheckMissed != 0 {
		t.Fatalf("misses that were not in a row ended the rounds: %+v", got)
	}
}

func TestTheDeadlineAsksTheOwner(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1d", "2026-11-14 12:00", "2026-11-13 09:00")
	setNow(at(13, 9, 0))
	if rounds := s.Recheck(at(13, 9, 0), nil); len(rounds) != 1 || rounds[0].Task.ResumeOn != "2026-11-14 09:00" {
		t.Fatalf("first round: %+v", rounds)
	}
	if _, _, err := s.Rechecked(item.ID, RecheckSame, "変化はありませんでした。", ""); err != nil {
		t.Fatal(err)
	}
	// The round after the last one would pass the deadline: the task
	// comes back at the deadline instead.
	setNow(at(14, 9, 0))
	rounds := s.Recheck(at(14, 9, 0), nil)
	if len(rounds) != 1 || rounds[0].Asked != "2026-11-14 09:00" || rounds[0].Task.ResumeOn != "2026-11-14 12:00" {
		t.Fatalf("last round: %+v", rounds)
	}
	last, _, err := s.Rechecked(item.ID, RecheckSame, "変化はありませんでした。", "")
	if err != nil {
		t.Fatal(err)
	}
	if state := last.RecheckState(); state != "最終確認 11/14 09:00、変化なし。次は 11/14 12:00 の期限で、要対応に出ます" {
		t.Fatalf("state before the deadline: %q", state)
	}
	setNow(at(14, 12, 0))
	rounds = s.Recheck(at(14, 12, 0), nil)
	if len(rounds) != 1 || rounds[0].Asked != "" || rounds[0].Task.Status != Review || rounds[0].Task.Rechecks() || rounds[0].Task.ResumeOn != "" {
		t.Fatalf("at the deadline: %+v", rounds)
	}
	if log := historyOf(t, s, rounds[0].Task); !strings.Contains(log, "確認の期限（2026-11-14 12:00）になりました") || !strings.Contains(log, "最後に確かめられたのは 2026-11-14 09:00") {
		t.Fatalf("history: %s", log)
	}
}

func TestAChangeEndsTheRoundsAndAsksTheOwner(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
	setNow(at(13, 9, 0))
	s.Recheck(at(13, 9, 0), nil)
	if _, _, err := s.Rechecked(item.ID, "done", "終わりました。", ""); err == nil {
		t.Fatal("accepted a result that does not exist")
	}
	if _, _, err := s.Rechecked(item.ID, RecheckSame, " ", ""); err == nil {
		t.Fatal("accepted a result that says nothing")
	}
	got, changed, err := s.Rechecked(item.ID, RecheckChanged, "審査の担当の方から、修正の依頼が届きました。", "side")
	if err != nil || !changed || got.Status != Review || got.Rechecks() || got.ResumeOn != "" || got.RecheckAsked != "" || got.ReadySince != "" {
		t.Fatalf("changed: %+v changed=%v err=%v", got, changed, err)
	}
	if log := historyOf(t, s, got); !strings.Contains(log, "確認の結果は「変化あり」です。審査の担当の方から") || !strings.Contains(log, "繰り返しの確認を止めます") {
		t.Fatalf("history: %s", log)
	}
	// Nothing is left to bring the task back by itself.
	setNow(at(14, 9, 0))
	if rounds := s.Recheck(at(14, 9, 0), nil); len(rounds) != 0 {
		t.Fatalf("asked after the rounds ended: %+v", rounds)
	}
	if _, _, err := s.Rechecked(item.ID, RecheckSame, "変化はありませんでした。", ""); err == nil || !strings.Contains(err.Error(), "登録がありません") {
		t.Fatalf("a result for a task with no rounds: %v", err)
	}
}

func TestLeavingWaitingEndsTheRounds(t *testing.T) {
	leave := map[string]func(*Store, string) error{
		"status": func(s *Store, id string) error {
			_, _, err := s.SetStatus(id, Doing, "", "作業を再開します。")
			return err
		},
		"claim": func(s *Store, id string) error {
			_, _, err := s.Claim(id, Owner{Label: "local-codex", Session: "k"}, "", "")
			return err
		},
		"ready": func(s *Store, id string) error {
			_, _, err := s.Ready(id, "待っていた処理が終わりました。", false)
			return err
		},
		"stop": func(s *Store, id string) error {
			_, _, err := s.Stop(id, "同じ書き込みが続きました。")
			return err
		},
		"close": func(s *Store, id string) error { _, _, err := s.SetStatus(id, Done, "", ""); return err },
	}
	for name, how := range leave {
		t.Run(name, func(t *testing.T) {
			s, item, _ := recheckStore(t)
			mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
			if err := how(s, item.ID); err != nil {
				t.Fatal(err)
			}
			got, _ := s.Get(item.ID)
			if got.Status == Waiting || got.Rechecks() || got.RecheckUntil != "" || got.ResumeOn != "" {
				t.Fatalf("the rounds outlived the wait: %+v", got)
			}
			if !strings.Contains(historyOf(t, s, got), "外部待ちを離れたので、繰り返しの確認を止めました。") {
				t.Fatalf("the history does not say so: %s", historyOf(t, s, got))
			}
			if warnings := StateWarnings(got, nil, StateContext{Now: at(13, 0, 0)}); len(warnings) != 0 {
				t.Fatalf("warnings after leaving: %v", warnings)
			}
		})
	}
}

func TestOnlyTheNextRoundCanBeChangedByHand(t *testing.T) {
	s, item, _ := recheckStore(t)
	other, _ := s.Create(NewTask{Area: "work", Title: "別のタスク"})
	mustRecheck(t, s, item.ID, "1d", "2026-12-01", "2026-11-13 09:00")
	// The reviewer wrote that the answer comes on Monday.
	got, changed, err := s.SetField(item.ID, "resume_on", "2026-11-16 09:00", "side")
	if err != nil || !changed || got.ResumeOn != "2026-11-16 09:00" || !got.Rechecks() {
		t.Fatalf("moving the next round: %+v changed=%v err=%v", got, changed, err)
	}
	if _, _, err := s.SetField(item.ID, "resume_on", "2026-12-02", ""); err == nil || !strings.Contains(err.Error(), "期限") {
		t.Fatalf("a next round after the deadline: %v", err)
	}
	if _, _, err := s.SetField(item.ID, "resume_on", "", ""); err == nil || !strings.Contains(err.Error(), "lacia task set "+item.ID) {
		t.Fatalf("removing the next round: %v", err)
	}
	for _, c := range []Field{{"after", other.ID}, {"after_run", "S1001"}, {"review_at", "2026-11-20"}, {"on_ready", "start"}} {
		if _, _, err := s.SetField(item.ID, c.Key, c.Value, ""); err == nil || !strings.Contains(err.Error(), "一緒に使えません") {
			t.Errorf("%s together with the rounds: %v", c.Key, err)
		}
	}
	if got, _ := s.Get(item.ID); got.ResumeOn != "2026-11-16 09:00" || got.HasReadyConditions() != true || len(got.After) != 0 {
		t.Fatalf("a refused change was written: %+v", got)
	}
}

func TestARoundWaitsForTheLookBeforeIt(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1h", "2026-11-20", "2026-11-12 11:00")
	setNow(at(12, 11, 0))
	if rounds := s.Recheck(at(12, 11, 0), nil); len(rounds) != 1 {
		t.Fatal("first round was not asked")
	}
	running := func(id, asked string) bool { return id == item.ID && asked == "2026-11-12 11:00" }
	// The look of 11:00 is still in the queue at 12:00: not a miss yet.
	setNow(at(12, 12, 30))
	if rounds := s.Recheck(at(12, 12, 30), running); len(rounds) != 0 {
		t.Fatalf("asked again while the look before was running: %+v", rounds)
	}
	if got, _ := s.Get(item.ID); got.RecheckMissed != 0 || got.ResumeOn != "2026-11-12 12:00" {
		t.Fatalf("counted a look that was still running: %+v", got)
	}
	// An hour past the round, the look counts as one without a result.
	setNow(at(12, 13, 0))
	rounds := s.Recheck(at(12, 13, 0), running)
	if len(rounds) != 1 || rounds[0].Asked != "2026-11-12 12:00" || rounds[0].Task.RecheckMissed != 1 || rounds[0].Task.ResumeOn != "2026-11-12 14:00" {
		t.Fatalf("after the grace: %+v", rounds)
	}
}

func TestARoundThatCouldNotBeAskedGoesToReview(t *testing.T) {
	s, item, setNow := recheckStore(t)
	mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
	setNow(at(13, 9, 0))
	s.Recheck(at(13, 9, 0), nil)
	got, changed, err := s.RecheckNotAsked(item.ID, "このサーバーは、会話への依頼を受け付けていません")
	if err != nil || !changed || got.Status != Review || got.Rechecks() || got.ResumeOn != "" {
		t.Fatalf("not asked: %+v changed=%v err=%v", got, changed, err)
	}
	if log := historyOf(t, s, got); !strings.Contains(log, "確認を別の会話に頼めませんでした（このサーバーは、会話への依頼を受け付けていません）") {
		t.Fatalf("history: %s", log)
	}
}

func TestLintPointsOutRoundsThatDoNotRun(t *testing.T) {
	s, item, _ := recheckStore(t)
	got := mustRecheck(t, s, item.ID, "1d", "2027-01-15", "2026-11-13 09:00")
	// Nobody moved the round on for more than one interval.
	warnings := StateWarnings(got, nil, StateContext{Now: at(14, 9, 1)})
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "間隔1回分より長く過ぎています") {
		t.Fatalf("overdue round: %v", warnings)
	}
	stale := got
	stale.Status = Review
	found := false
	for _, w := range StateWarnings(stale, nil, StateContext{Now: at(12, 11, 0)}) {
		found = found || strings.Contains(w.Message, "繰り返しの確認の項目（recheck_every など）が残っています")
	}
	if !found {
		t.Fatal("fields left on a task that does not wait are not pointed out")
	}
}
