package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	routineActionsField = "x_routine_actions"
	routineActionsStart = "<!-- lacia:routine-actions -->"
	routineActionsEnd   = "<!-- /lacia:routine-actions -->"
	routineActionActor  = "定期タスクの実行結果"
)

// RoutineAction is an item the server maintains inside a routine's task.
// Its key is stable across retries and new observations of the same matter.
type RoutineAction struct {
	Key        string    `json:"key"`
	Scope      string    `json:"scope,omitempty"`
	Text       string    `json:"text"`
	Reference  string    `json:"reference,omitempty"`
	Revision   string    `json:"revision,omitempty"`
	LinkedTask string    `json:"linked_task,omitempty"`
	Owner      bool      `json:"owner,omitempty"`
	Technical  bool      `json:"technical,omitempty"`
	First      time.Time `json:"first"`
	Last       time.Time `json:"last"`
	Count      int       `json:"count"`
}

// RoutineObservation describes the state observed in one ordered execution.
type RoutineObservation struct {
	At             time.Time
	Actions        []RoutineAction
	Verified       []string
	ClearTechnical bool
	Resolved       []string
	Check          bool // a health observation outside the normal execution
	Silent         bool // an already delivered result carried over at cutover
	// Shared marks matters another routine sends to this task. They are
	// ordered item by item and leave this routine's own run clock alone, so
	// neither routine's late result hides the other's.
	Shared bool
}

type routineActionState struct {
	RunAt         time.Time                `json:"run_at,omitzero"`
	Items         map[string]RoutineAction `json:"items,omitempty"`
	ManagedReview bool                     `json:"managed_review,omitempty"`
	Notice        uint64                   `json:"notice,omitempty"`
	Notified      uint64                   `json:"notified,omitempty"`
}

func readRoutineActions(d Doc) (routineActionState, error) {
	state := routineActionState{}
	if raw := d.Get(routineActionsField); raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return state, fmt.Errorf("定期タスクの対応を読めませんでした: %w", err)
		}
	}
	if state.Items == nil {
		state.Items = map[string]RoutineAction{}
	}
	return state, nil
}

func actionList(state routineActionState) []RoutineAction {
	var items []RoutineAction
	for _, item := range state.Items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Owner != items[j].Owner {
			return items[i].Owner
		}
		return items[i].Key < items[j].Key
	})
	return items
}

// RoutineActions returns the current managed items in display order.
func (t Task) RoutineActions() []RoutineAction {
	return actionList(t.routineActions)
}

// RoutineActionSummary returns the first current action for the home page.
// Only server-managed review replaces the opening line of a hand-written task.
func (t Task) RoutineActionSummary() string {
	if t.Status == Review && t.routineActions.ManagedReview {
		for _, item := range actionList(t.routineActions) {
			if item.Owner {
				return item.Text
			}
		}
	}
	return ""
}

// RoutineActionNotice is the next outstanding notification for owner action.
func (t Task) RoutineActionNotice() (uint64, string, int) {
	if t.Status != Review || t.routineActions.Notice <= t.routineActions.Notified {
		return 0, "", 0
	}
	var text string
	count := 0
	for _, item := range actionList(t.routineActions) {
		if item.Owner {
			if text == "" {
				text = item.Text
			}
			count++
		}
	}
	if count == 0 {
		text = strings.TrimSpace(strings.SplitN(t.Next(), "\n\n", 2)[0])
		if text == "" {
			return 0, "", 0
		}
		count = 1
	}
	return t.routineActions.Notice, text, count
}

func withoutRoutineActionBlock(body string) string {
	start := strings.Index(body, routineActionsStart)
	if start < 0 {
		return body
	}
	end := strings.Index(body[start+len(routineActionsStart):], routineActionsEnd)
	if end < 0 {
		return body
	}
	end += start + len(routineActionsStart) + len(routineActionsEnd)
	if start > 0 && strings.HasSuffix(body[:start], "\n\n") && strings.HasPrefix(body[end:], "\n\n") {
		end += 2
	}
	return body[:start] + body[end:]
}

var escapeActionMarkdown = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;", "`", "\\`", "*", "\\*", "_", "\\_")

func withRoutineActionBlock(body string, state routineActionState) string {
	body = withoutRoutineActionBlock(body)
	if len(state.Items) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(routineActionsStart + "\n\n実行から分かった対応\n\n")
	linked := map[string]bool{}
	for _, action := range actionList(state) {
		if action.LinkedTask != "" {
			if linked[action.LinkedTask] {
				continue
			}
			linked[action.LinkedTask] = true
		}
		text := escapeActionMarkdown.Replace(strings.Join(strings.Fields(action.Text), " "))
		fmt.Fprintf(&b, "- %s", text)
		if action.Count > 1 && action.Technical {
			fmt.Fprintf(&b, "（%d回）", action.Count)
		}
		if link := routineActionLink(action.Reference); link != "" {
			fmt.Fprintf(&b, " [根拠](<%s>)", link)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n" + routineActionsEnd)
	const heading = "## 次にすること\n"
	if at := strings.Index(body, heading); at >= 0 {
		insert := at + len(heading)
		return body[:insert] + "\n" + b.String() + "\n\n" + strings.TrimLeft(body[insert:], "\n")
	}
	return strings.TrimRight(body, "\n") + "\n\n" + heading + "\n" + b.String() + "\n"
}

func routineActionLink(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.User != nil || strings.ContainsAny(link, "\r\n<>") {
		return ""
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//") ||
		(u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return u.String()
	}
	return ""
}

// MergeRoutineActions changes only the server-managed part of task.md and
// applies observations in schedule order. The single file update commits the
// action items and their projection together.
func (s *Store) MergeRoutineActions(id string, observation RoutineObservation) (Task, error) {
	if s.noTasks {
		return Task{}, ErrNoTasks
	}
	if !ValidRoutineID(id) || observation.At.IsZero() {
		return Task{}, fmt.Errorf("定期タスクのIDと観測時刻が必要です")
	}
	if t, err := s.Get(id); err == nil && !t.Routine {
		return Task{}, ErrNotFound
	}
	if _, err := s.Get(id); errors.Is(err, ErrNotFound) {
		if len(observation.Actions) == 0 {
			return Task{}, nil
		}
		const body = "## ひとことで\n\n定期タスクの実行で見つかった用件を、ここにまとめます。\n\n## 次にすること\n\n次の実行で結果を確かめます。\n"
		if _, err := s.createRoutineTask(id, body, routineActionActor); err != nil {
			if _, getErr := s.Get(id); getErr != nil {
				return Task{}, err
			}
		}
	} else if err != nil {
		return Task{}, err
	}
	t, _, err := s.update(id, func(doc *Doc, _ time.Time) (Event, error) {
		state, err := readRoutineActions(*doc)
		if err != nil {
			return Event{}, err
		}
		if !observation.Check && !observation.Shared {
			if !observation.At.After(state.RunAt) {
				return Event{}, nil
			}
			state.RunAt = observation.At
		}
		changed, notify := applyRoutineObservation(&state, observation)
		if !changed {
			return Event{}, nil
		}
		if notify && !observation.Silent {
			state.Notice++
		}
		before := Status(doc.Get("status"))
		after := before
		ownerNeeded := false
		for _, item := range state.Items {
			ownerNeeded = ownerNeeded || item.Owner
		}
		if ownerNeeded && before == Waiting {
			after, state.ManagedReview = Review, true
		}
		if !ownerNeeded && before == Review && state.ManagedReview {
			after, state.ManagedReview = Waiting, false
		}
		if changed {
			doc.Body = withRoutineActionBlock(doc.Body, state)
		}
		doc.Set("status", string(after))
		if err := saveRoutineActionState(doc, state); err != nil {
			return Event{}, err
		}
		if before != after {
			return Event{Type: EventStatus, By: routineActionActor, From: string(before), To: string(after), Text: statusChangeText(before, after, "")}, nil
		}
		return Event{Type: EventFile, By: routineActionActor, Action: "wrote", Path: TaskFile, Text: "定期タスクの実行結果を対応に照合しました。"}, nil
	})
	return t, err
}

func applyRoutineObservation(state *routineActionState, observation RoutineObservation) (changed, notify bool) {
	active := map[string]bool{}
	for _, action := range observation.Actions {
		active[action.Key] = true
	}
	for key, item := range state.Items {
		if active[key] || observation.Shared && item.Last.After(observation.At) {
			continue
		}
		if slices.Contains(observation.Resolved, key) || item.Technical && strings.HasPrefix(key, "run:") &&
			(observation.ClearTechnical || slices.Contains(observation.Verified, item.Scope)) {
			delete(state.Items, key)
			changed = true
		}
	}
	for _, item := range observation.Actions {
		if item.Key == "" || item.Text == "" {
			continue
		}
		if previous, ok := state.Items[item.Key]; ok {
			if previous.Last.After(observation.At) {
				continue
			}
			if item.Revision == previous.Revision {
				if observation.Check || !item.Technical {
					continue
				}
				item.First, item.Count = previous.First, previous.Count+max(item.Count, 1)
			} else {
				item.First, item.Count = observation.At, max(item.Count, 1)
				notify = notify || item.Owner && !previous.Owner
			}
		} else {
			item.First, item.Count = observation.At, max(item.Count, 1)
			notify = notify || item.Owner
		}
		item.Last = observation.At
		state.Items[item.Key] = item
		changed = true
	}
	return changed, notify
}

func saveRoutineActionState(doc *Doc, state routineActionState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw)+len(doc.Body) > MaxDocSize {
		return fmt.Errorf("定期タスクの対応が %s を超えています", HumanSize(MaxDocSize))
	}
	doc.Set(routineActionsField, string(raw))
	return nil
}

func (s *Store) MarkRoutineActionNotified(id string, sequence uint64) error {
	_, _, err := s.update(id, func(doc *Doc, _ time.Time) (Event, error) {
		state, err := readRoutineActions(*doc)
		if err != nil || sequence <= state.Notified {
			return Event{}, err
		}
		if sequence > state.Notice {
			return Event{}, fmt.Errorf("定期タスクの対応通知が見つかりません")
		}
		state.Notified = sequence
		if err := saveRoutineActionState(doc, state); err != nil {
			return Event{}, err
		}
		return Event{Type: EventNote, By: routineActionActor, Text: "定期タスクの対応の通知を記録しました。"}, nil
	})
	return err
}
