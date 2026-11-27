package task

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A task that waits for something outside can be looked at again and again
// until that something comes. The task says how often (recheck_every) and
// until when (recheck_until). The server keeps the time of the next look in
// resume_on and moves it on before it asks a conversation to look, so the
// rounds go on even when a conversation forgets or fails. See
// docs/design/records.md, "外部の変化を繰り返し確かめる".

// Results of one look, as `lacia task rechecked` takes them.
const (
	RecheckSame    = "same"    // looked, and nothing the owner has to see
	RecheckChanged = "changed" // something the owner has to see
	RecheckUnknown = "unknown" // could not look
)

// Actions of an EventRecheck.
const (
	RecheckRegistered = "registered"
	RecheckAsked      = "asked"
	RecheckExpired    = "expired"
	RecheckMissed     = "missed"
	RecheckFailed     = "failed"
)

// MinRecheckEvery is the shortest time between two looks. Every look
// starts a conversation, so a shorter one would cost more than it tells.
const MinRecheckEvery = time.Hour

// MaxRecheckMisses is how many rounds in a row may end without a look that
// worked before the owner is asked.
const MaxRecheckMisses = 2

// recheckGrace is how long a round that became due waits for the look of
// the round before it, when that one is still in the queue or running.
const recheckGrace = time.Hour

// recheckKeys are the fields of the registration. They go when the task
// stops waiting, together with resume_on, which holds the next round.
var recheckKeys = []string{"recheck_every", "recheck_until", "recheck_asked", "recheck_missed", "recheck_last"}

var everyPattern = regexp.MustCompile(`^([1-9][0-9]{0,3})(h|d)$`)

// ParseEvery reads how often to look: hours ("3h") or days ("1d").
func ParseEvery(s string) (time.Duration, error) {
	m := everyPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("確認の間隔 %q を読めません。1d（毎日）や 3h（3時間ごと）のように、時間（h）か日（d）で書いてください", s)
	}
	n, _ := strconv.Atoi(m[1])
	every := time.Duration(n) * time.Hour
	if m[2] == "d" {
		every *= 24
	}
	if every < MinRecheckEvery {
		return 0, errors.New("確認の間隔は1時間以上にしてください")
	}
	return every, nil
}

// EveryLabel says the interval the way a person reads it: "毎日", "3時間ごと".
func EveryLabel(value string) string {
	m := everyPattern.FindStringSubmatch(value)
	if m == nil {
		return value
	}
	switch {
	case m[2] == "d" && m[1] == "1":
		return "毎日"
	case m[2] == "d":
		return m[1] + "日ごと"
	}
	return m[1] + "時間ごと"
}

// addEvery moves a time on by one interval. Whole days keep the time of
// day in zones that change their clocks.
func addEvery(t time.Time, value string) time.Time {
	m := everyPattern.FindStringSubmatch(value)
	if m == nil {
		return t
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "d" {
		return t.AddDate(0, 0, n)
	}
	return t.Add(time.Duration(n) * time.Hour)
}

// Rechecks reports whether the task is looked at again and again while it
// waits.
func (t Task) Rechecks() bool { return t.RecheckEvery != "" }

func (t Task) location() *time.Location { return (Config{Timezone: t.Timezone}).Location() }

// shortWhen writes a stored date or time as "11/28 09:02".
func shortWhen(value string, loc *time.Location) string {
	when, err := ParseWhenIn(value, loc)
	if err != nil {
		return value
	}
	return when.Format("01/02 15:04")
}

// RecheckLabel says how often the task is looked at and until when, such
// as "毎日 09:00 に確認（2027-01-15 まで）".
func (t Task) RecheckLabel() string {
	if !t.Rechecks() {
		return ""
	}
	label := EveryLabel(t.RecheckEvery)
	if label == "毎日" {
		if next, err := ParseWhenIn(t.ResumeOn, t.location()); err == nil {
			label += " " + next.Format("15:04") + " "
		}
	}
	return label + "に確認（" + t.RecheckUntil + " まで）"
}

// RecheckState says what the looks have found so far and when the next one
// comes, such as "最終確認 11/28 09:02、変化なし。次回は 11/29 09:00".
func (t Task) RecheckState() string {
	if !t.Rechecks() {
		return ""
	}
	loc := t.location()
	var parts []string
	switch {
	case t.RecheckMissed > 0 && t.RecheckLast != "":
		parts = append(parts, "前回は確かめられませんでした（最後に確かめられたのは "+shortWhen(t.RecheckLast, loc)+"）")
	case t.RecheckMissed > 0:
		parts = append(parts, "前回は確かめられませんでした")
	case t.RecheckLast != "":
		parts = append(parts, "最終確認 "+shortWhen(t.RecheckLast, loc)+"、変化なし")
	case t.RecheckAsked == "":
		parts = append(parts, "初回の確認を待っています")
	}
	if t.RecheckAsked != "" {
		parts = append(parts, shortWhen(t.RecheckAsked, loc)+" の回の結果を待っています")
	}
	switch {
	case t.ResumeOn == "":
	case t.ResumeOn == t.RecheckUntil:
		// The round after the last one is the deadline itself.
		parts = append(parts, "次は "+shortWhen(t.ResumeOn, loc)+" の期限で、要対応に出ます")
	default:
		parts = append(parts, "次回は "+shortWhen(t.ResumeOn, loc))
	}
	return strings.Join(parts, "。")
}

// recheckHelp is the line of waitingConditionHelp about this way to wait.
func recheckHelp(id string) string {
	return fmt.Sprintf("- 外部の変化を繰り返し確かめながら待つ: lacia task recheck %s --every 1d --until 2026-12-31（登録すると、外部待ちになります）\n", id)
}

// recheckConflict refuses a condition that cannot go together with the
// rounds: the next round is the one thing that brings the task back.
func recheckConflict(id, key string) error {
	return fmt.Errorf("%s は、外部の変化を繰り返し確かめながら待っています。%s とは一緒に使えません。次の回だけを変えるときは lacia task field %s resume_on \"日時\" を使います。繰り返しの確認をやめて %s で待つときは、lacia task set %s doing で外部待ちを離れてから指定してください", id, key, id, key, id)
}

// RecheckRound is one round that became due.
type RecheckRound struct {
	Task Task
	// Asked is the round the server now asks a conversation to look at. It
	// is empty when the task went to review instead.
	Asked string
}

// SetRecheck registers how often and until when the task is looked at, and
// leaves the task waiting. next is the first round; without it, that is one
// interval from now.
func (s *Store) SetRecheck(ref, every, until, next, actor string) (Task, bool, error) {
	if s.noTasks {
		return Task{}, false, ErrNoTasks
	}
	current, err := s.Get(ref)
	if err != nil {
		return Task{}, false, err
	}
	loc := s.Location()
	every = strings.TrimSpace(every)
	if every == "" {
		return Task{}, false, errors.New("確認の間隔を --every で指定してください。ふだんは 1d（毎日）にします")
	}
	if _, err := ParseEvery(every); err != nil {
		return Task{}, false, err
	}
	if strings.TrimSpace(until) == "" {
		return Task{}, false, errors.New("確認を続ける期限を --until で指定してください。期限が来ると、" + OwnerName + "に続け方を伺うために「要対応」へ出します")
	}
	deadline, err := parseWhenIn(until, loc)
	if err != nil {
		return Task{}, false, err
	}
	switch {
	case current.Routine:
		return Task{}, false, ErrRoutineField
	case current.Status.Closed():
		return Task{}, false, fmt.Errorf("%s は、もう閉じています", current.ID)
	}
	return s.update(ref, func(doc *Doc, at time.Time) (Event, error) {
		id := doc.Get("id")
		if !deadline.After(at) {
			return Event{}, fmt.Errorf("期限 %s は、もう過ぎています", formatWhenIn(deadline, loc))
		}
		first := addEvery(at, every)
		if strings.TrimSpace(next) != "" {
			if first, err = parseWhenIn(next, loc); err != nil {
				return Event{}, err
			}
			if first.Before(at.Truncate(time.Minute)) {
				return Event{}, fmt.Errorf("最初の確認の日時 %s は、もう過ぎています", formatWhenIn(first, loc))
			}
		}
		if first.After(deadline) {
			return Event{}, fmt.Errorf("最初の確認（%s）が、期限（%s）より後になります。期限を延ばすか、--next で早い日時を指定してください", formatWhenIn(first, loc), formatWhenIn(deadline, loc))
		}
		for _, key := range []string{"after", "after_run", "review_at", "on_ready"} {
			if doc.Get(key) != "" {
				return Event{}, fmt.Errorf("%s には %s が指定されています。繰り返しの確認とは一緒に使えません。先に lacia task field %s %s で外してから、もう1度登録してください", id, key, id, key)
			}
		}
		old := Status(doc.Get("status"))
		when, limit := formatWhenIn(first, loc), formatWhenIn(deadline, loc)
		if old == Waiting && doc.Get("recheck_every") == every && doc.Get("recheck_until") == limit && doc.Get("resume_on") == when {
			return Event{}, nil
		}
		for _, key := range recheckKeys {
			doc.Set(key, "")
		}
		doc.Set("recheck_every", every)
		doc.Set("recheck_until", limit)
		doc.Set("resume_on", when)
		doc.Set("ready_since", "")
		doc.Set("ready_run_ref", "")
		e := Event{Type: EventRecheck, Action: RecheckRegistered, By: actor,
			Text: fmt.Sprintf("外部の変化を、%sに確かめながら待ちます。期限は %s で、最初の確認は %s です。", EveryLabel(every), limit, when)}
		if old != Waiting {
			doc.Set("status", string(Waiting))
			doc.Set("stopped_at", "")
			doc.Set("owner", "")
			doc.Set("owner_session", "")
			doc.Set("done", "")
			e.From, e.To = string(old), string(Waiting)
			e.Text += statusChangeText(old, Waiting, "")
		}
		return e, nil
	})
}

// Recheck moves on every waiting task whose next round has come. A task
// past its deadline, or with two rounds in a row that ended without a look
// that worked, goes to review. For the others the next round is written
// first, and the task comes back in a RecheckRound for the caller to ask a
// conversation to look. pending reports a round whose look has yet to
// finish; such a task waits up to recheckGrace before the round counts as
// one without a result.
func (s *Store) Recheck(now time.Time, pending func(id, asked string) bool) []RecheckRound {
	var rounds []RecheckRound
	loc := s.Location()
	for _, t := range s.List() {
		if t.Status != Waiting || !t.Rechecks() {
			continue
		}
		// A next round nobody can read is due now, so the task cannot
		// stay out of sight because of it.
		next, err := parseWhenIn(t.ResumeOn, loc)
		if err != nil || t.ResumeOn == "" {
			next = now
		}
		if next.After(now) {
			continue
		}
		if t.RecheckAsked != "" && pending != nil && now.Before(next.Add(recheckGrace)) && pending(t.ID, t.RecheckAsked) {
			continue
		}
		snapshot, asked := t, ""
		updated, changed, err := s.update(t.ID, func(doc *Doc, at time.Time) (Event, error) {
			if Status(doc.Get("status")) != Waiting || doc.Get("recheck_every") != snapshot.RecheckEvery || doc.Get("recheck_until") != snapshot.RecheckUntil ||
				doc.Get("resume_on") != snapshot.ResumeOn || doc.Get("recheck_asked") != snapshot.RecheckAsked {
				return Event{}, nil // edited meanwhile; look again next time
			}
			missed := snapshot.RecheckMissed
			if snapshot.RecheckAsked != "" {
				missed++
			}
			deadline, deadlineErr := parseWhenIn(snapshot.RecheckUntil, loc)
			_, everyErr := ParseEvery(snapshot.RecheckEvery)
			e := Event{Type: EventRecheck}
			switch {
			case deadlineErr != nil || everyErr != nil:
				e.Action = RecheckFailed
				e.Text = "確認の間隔か期限を読めないので、繰り返しの確認を続けられません。"
			case !deadline.After(now):
				e.Action = RecheckExpired
				e.Text = fmt.Sprintf("確認の期限（%s）になりました。", snapshot.RecheckUntil)
				if snapshot.RecheckLast != "" {
					e.Text += fmt.Sprintf("最後に確かめられたのは %s で、変化はありませんでした。", snapshot.RecheckLast)
				} else {
					e.Text += "変化なしと確かめられた回は、ありませんでした。"
				}
				e.Text += "待ち続けるか、やめるかを決めていただくために、繰り返しの確認を止めます。"
			case missed >= MaxRecheckMisses:
				e.Action = RecheckMissed
				e.Text = fmt.Sprintf("確認が%d回続けて、結果を残さずに終わったか、対象を確かめられませんでした。", missed)
				if snapshot.RecheckLast != "" {
					e.Text += fmt.Sprintf("最後に確かめられたのは %s です。", snapshot.RecheckLast)
				}
				e.Text += "確認の方法を見直していただくために、繰り返しの確認を止めます。"
			default:
				following := next
				for !following.After(now) {
					following = addEvery(following, snapshot.RecheckEvery)
				}
				if following.After(deadline) {
					following = deadline
				}
				asked = formatWhenIn(next, loc)
				doc.Set("resume_on", formatWhenIn(following, loc))
				doc.Set("recheck_asked", asked)
				doc.Set("recheck_missed", strconv.Itoa(missed))
				if missed == 0 {
					doc.Set("recheck_missed", "")
				}
				e.Action = RecheckAsked
				e.Text = fmt.Sprintf("決めてあった確認の回（%s）になりました。別の会話に、読み取りの確認を頼みます。次の回は %s です。", asked, formatWhenIn(following, loc))
				if snapshot.RecheckAsked != "" {
					e.Text += fmt.Sprintf("前の回（%s）は、結果が残りませんでした。", snapshot.RecheckAsked)
				}
				return e, nil
			}
			clearRecheck(doc)
			doc.Set("status", string(Review))
			doc.Set("ready_since", "")
			e.From, e.To = string(Waiting), string(Review)
			e.Text += statusChangeText(Waiting, Review, "")
			return e, nil
		})
		if err == nil && changed {
			rounds = append(rounds, RecheckRound{Task: updated, Asked: asked})
		}
	}
	return rounds
}

// RecheckNotAsked takes back a round the server could not hand to a
// conversation: the task goes to review, with the reason in its history.
func (s *Store) RecheckNotAsked(ref, why string) (Task, bool, error) {
	return s.update(ref, func(doc *Doc, at time.Time) (Event, error) {
		if Status(doc.Get("status")) != Waiting || doc.Get("recheck_every") == "" {
			return Event{}, nil
		}
		clearRecheck(doc)
		doc.Set("status", string(Review))
		doc.Set("ready_since", "")
		return Event{Type: EventRecheck, Action: RecheckFailed, From: string(Waiting), To: string(Review),
			Text: "確認を別の会話に頼めませんでした（" + strings.TrimSpace(why) + "）。繰り返しの確認を止めます。" + statusChangeText(Waiting, Review, "")}, nil
	})
}

// Rechecked records what one look found. "same" and "unknown" leave the
// task waiting; "changed" ends the rounds and moves it to review.
func (s *Store) Rechecked(ref, result, text, actor string) (Task, bool, error) {
	if s.noTasks {
		return Task{}, false, ErrNoTasks
	}
	text = strings.TrimSpace(text)
	switch result {
	case RecheckSame, RecheckChanged, RecheckUnknown:
	default:
		return Task{}, false, fmt.Errorf("確認の結果は same（変化なし）、changed（変化あり）、unknown（確かめられなかった）のどれかで指定してください")
	}
	if text == "" {
		return Task{}, false, errors.New("何を確かめて、どうだったのかを、結果のあとに1〜2文で書いてください")
	}
	loc := s.Location()
	return s.update(ref, func(doc *Doc, at time.Time) (Event, error) {
		id, status := doc.Get("id"), Status(doc.Get("status"))
		if status != Waiting || doc.Get("recheck_every") == "" {
			return Event{}, fmt.Errorf("%s には、繰り返しの確認の登録がありません（状態は「%s」です）。結果は記録していません。履歴に残すときは lacia task note %s を使ってください", id, status.Label(), id)
		}
		e := Event{Type: EventRecheck, Action: result, By: actor}
		switch result {
		case RecheckSame:
			doc.Set("recheck_asked", "")
			doc.Set("recheck_missed", "")
			doc.Set("recheck_last", at.In(loc).Format("2006-01-02 15:04"))
			e.Text = "確認の結果は「変化なし」です。" + text
		case RecheckUnknown:
			missed, _ := strconv.Atoi(doc.Get("recheck_missed"))
			doc.Set("recheck_asked", "")
			doc.Set("recheck_missed", strconv.Itoa(missed+1))
			e.Text = "確認の結果は「確かめられなかった」です。" + text
		case RecheckChanged:
			clearRecheck(doc)
			doc.Set("status", string(Review))
			doc.Set("ready_since", "")
			e.From, e.To = string(Waiting), string(Review)
			e.Text = "確認の結果は「変化あり」です。" + text + "\n繰り返しの確認を止めます。" + statusChangeText(Waiting, Review, "")
		}
		return e, nil
	})
}

// clearRecheck removes the registration and the next round with it.
func clearRecheck(doc *Doc) {
	for _, key := range recheckKeys {
		doc.Set(key, "")
	}
	doc.Set("resume_on", "")
}
