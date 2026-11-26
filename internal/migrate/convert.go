package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/imos/lacia/internal/task"
)

// The shapes of log.md that become events. See the design of format 1.
var (
	entryLine  = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}) (.*)$`)
	datedStart = regexp.MustCompile(`^(?:- |#+ )(\d{4}-\d{2}-\d{2})`)
	anyDate    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	writer     = regexp.MustCompile(`（(Web|[a-z]+-(?:codex|claude)(?: [0-9]+)?)）$`)

	createdText = regexp.MustCompile(`^タスクを作りました。状態は「([^「」]*)」です。`)
	statusText  = regexp.MustCompile(`^状態を「([^「」]*)」から「([^「」]*)」に変えました。(?:担当は (\S+) です。)?`)
	claimText   = regexp.MustCompile(`^担当を (\S+) にしました。`)
	takeText    = regexp.MustCompile(`^担当を (\S+) から (\S+) に替えました。`)
	requestText = regexp.MustCompile(`^依頼を受け付けました（([^（）、]+)、([^（）、]+)）。`)
	putText     = regexp.MustCompile(`^((?:files|artifacts)/\S+) を置きました（`)
	hashText    = regexp.MustCompile(`sha256 ([0-9a-f]+)`)
	removeText  = regexp.MustCompile(`^(files/\S+) を消しました。`)
	setText     = regexp.MustCompile(`^([a-z_]+) を「([^「」]*)」にしました。`)
	unsetText   = regexp.MustCompile(`^([a-z_]+) を消しました。`)
)

// fields are the front matter keys a "field" event may name.
var fields = map[string]bool{"id": true, "title": true, "status": true, "created": true, "done": true, "due": true, "parent": true, "priority": true, "project": true, "resume_on": true, "review_at": true, "after": true, "on_ready": true, "ready_since": true, "stopped_at": true, "owner": true, "owner_session": true}

// statusOf turns the word shown for a status back into its name.
func statusOf(label string) (string, bool) {
	for _, s := range task.Statuses {
		if s.Label() == label {
			return string(s), true
		}
	}
	return "", false
}

// classify decides the kind of an event moved from a task's log.md.
func classify(e *task.Event) {
	text := e.Text
	e.Type = task.EventNote
	if m := createdText.FindStringSubmatch(text); m != nil {
		if s, ok := statusOf(m[1]); ok {
			e.Type, e.Status = task.EventCreated, s
		}
		return
	}
	if m := statusText.FindStringSubmatch(text); m != nil {
		from, ok1 := statusOf(m[1])
		to, ok2 := statusOf(m[2])
		if ok1 && ok2 {
			e.Type, e.From, e.To, e.Owner = task.EventStatus, from, to, m[3]
		}
		return
	}
	if m := takeText.FindStringSubmatch(text); m != nil {
		e.Type, e.Previous, e.Owner = task.EventClaimed, m[1], m[2]
		return
	}
	if m := claimText.FindStringSubmatch(text); m != nil {
		e.Type, e.Owner = task.EventClaimed, m[1]
		return
	}
	if m := requestText.FindStringSubmatch(text); m != nil {
		e.Type, e.Via, e.Request = task.EventRequest, m[1], m[2]
		return
	}
	if m := putText.FindStringSubmatch(text); m != nil {
		e.Type, e.Action, e.Path = task.EventFile, "put", m[1]
		if h := hashText.FindStringSubmatch(text); h != nil {
			e.SHA256 = h[1]
		}
		return
	}
	if m := removeText.FindStringSubmatch(text); m != nil {
		e.Type, e.Action, e.Path = task.EventFile, "removed", m[1]
		return
	}
	if m := setText.FindStringSubmatch(text); m != nil && fields[m[1]] {
		value := m[2]
		e.Type, e.Key, e.Value = task.EventField, m[1], &value
		return
	}
	if m := unsetText.FindStringSubmatch(text); m != nil && fields[m[1]] {
		empty := ""
		e.Type, e.Key, e.Value = task.EventField, m[1], &empty
	}
}

func blank(line string) bool { return strings.TrimSpace(line) == "" }

// Convert turns one log.md into the lines of a log.jsonl, keeping every
// character of the text. isTask decides whether the dated lines get a kind
// from their sentence; fallback is the day a block without a date of its
// own gets when no event comes before it. It checks the result against the
// source and fails when they differ.
func Convert(src string, isTask bool, fallback string) (data []byte, events, blocks int, err error) {
	lines := strings.Split(src, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := 0
	if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") && strings.HasSuffix(lines[0], "の履歴") {
		start = 1
	}
	var out []task.Event
	last := ""
	if fallback != "" {
		last = fallback + "T00:00:00+09:00"
	}
	var block []string
	flush := func() {
		for len(block) > 0 && blank(block[0]) {
			block = block[1:]
		}
		for len(block) > 0 && blank(block[len(block)-1]) {
			block = block[:len(block)-1]
		}
		if len(block) == 0 {
			return
		}
		at := last
		if m := datedStart.FindStringSubmatch(block[0]); m != nil {
			at = m[1] + "T00:00:00+09:00"
		}
		out = append(out, task.Event{At: at, Type: task.EventLegacy, Text: strings.Join(block, "\n"), Imported: true})
		last = at
		blocks++
		block = nil
	}
	var kept []string // the source lines the events must reproduce
	for _, line := range lines[start:] {
		if m := entryLine.FindStringSubmatch(line); m != nil {
			flush()
			e := task.Event{At: m[1] + "T" + m[2] + ":00+09:00", Text: m[3], Imported: true}
			if isTask {
				classify(&e)
			} else {
				e.Type = task.EventNote
			}
			if w := writer.FindStringSubmatch(e.Text); w != nil {
				e.By = w[1]
			}
			out = append(out, e)
			last = e.At
			events++
			kept = append(kept, m[3])
			continue
		}
		if datedStart.MatchString(line) && strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "#") {
			flush()
		}
		block = append(block, line)
		if !blank(line) {
			kept = append(kept, line)
		}
	}
	flush()
	var buf bytes.Buffer
	var got []string
	for _, e := range out {
		if e.At == "" {
			return nil, 0, 0, fmt.Errorf("日付の分からない記録があります。最初の行: %s", firstLine(e.Text))
		}
		line, err := task.MarshalEvent(e)
		if err != nil {
			return nil, 0, 0, err
		}
		var back task.Event
		if err := json.Unmarshal(line, &back); err != nil {
			return nil, 0, 0, err
		}
		for _, l := range strings.Split(back.Text, "\n") {
			if !blank(l) {
				got = append(got, l)
			}
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if strings.Join(got, "\n") != strings.Join(kept, "\n") {
		return nil, 0, 0, fmt.Errorf("変換した履歴の文が、もとの log.md と一致しません")
	}
	return buf.Bytes(), events, blocks, nil
}

// FirstDate is the first date written in a text, or "".
func FirstDate(src string) string { return anyDate.FindString(src) }

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
