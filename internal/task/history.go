package task

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imos/lacia/internal/effect/disk"
)

// HistoryFile holds the history of a task or a project, one JSON object per
// event. LogFile is where format 0 kept it, one dated line per entry.
const (
	LogFile     = "log.md"
	HistoryFile = "log.jsonl"
)

// Kinds of events. See docs/design/records.md.
const (
	EventCreated  = "created"
	EventStatus   = "status"
	EventClaimed  = "claimed"
	EventField    = "field"
	EventFile     = "file"
	EventRequest  = "request"
	EventNote     = "note"
	EventWoke     = "woke"
	EventStopped  = "stopped"
	EventPeer     = "peer"
	EventWait     = "wait"
	EventHandover = "handover"
	EventLegacy   = "legacy"
	EventOrder    = "order"
	EventRecheck  = "recheck"
)

// MaxHandoverBody is how much of task.md a handover event keeps.
const MaxHandoverBody = 100000

// WakeClearedConditions marks a wake that cleared the task's ready conditions.
const WakeClearedConditions = "cleared_conditions"

// Event is one entry of a history. The fields are written in this order;
// empty ones are left out. Text is the sentence a person reads, without who
// wrote it: that is By.
type Event struct {
	At        string  `json:"at"`
	Type      string  `json:"type"`
	By        string  `json:"by,omitempty"`
	Status    string  `json:"status,omitempty"`
	From      string  `json:"from,omitempty"`
	To        string  `json:"to,omitempty"`
	Owner     string  `json:"owner,omitempty"`
	Session   string  `json:"session,omitempty"`
	Previous  string  `json:"previous,omitempty"`
	Key       string  `json:"key,omitempty"`
	Value     *string `json:"value,omitempty"`
	Action    string  `json:"action,omitempty"`
	Path      string  `json:"path,omitempty"`
	Bytes     *int64  `json:"bytes,omitempty"`
	SHA256    string  `json:"sha256,omitempty"`
	Request   string  `json:"request,omitempty"`
	Via       string  `json:"via,omitempty"`
	Wait      string  `json:"wait,omitempty"`
	Handover  string  `json:"handover,omitempty"`
	Task      string  `json:"task,omitempty"`
	Direction string  `json:"direction,omitempty"`
	Body      string  `json:"body,omitempty"`
	Text      string  `json:"text,omitempty"`
	Imported  bool    `json:"imported,omitempty"`

	// After is what a line of log.md puts after who wrote it, such as the
	// note added to a change of status. In log.jsonl it ends Text.
	After string `json:"-"`
}

// text is the sentence of the event in log.jsonl: the rest follows the
// sentence directly, as there is no writer in brackets between them.
func (e Event) text() string {
	return strings.TrimSpace(e.Text + e.After)
}

// MarshalEvent writes one line of log.jsonl, without the newline. Text is
// kept as it reads: no \u escapes for Japanese or for <, > and &.
func MarshalEvent(e Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// AppendEvent adds one event to the log.jsonl of that name in the records.
func AppendEvent(records *disk.Dir, name string, now time.Time, e Event) error {
	line, err := eventLine(now, e)
	if err != nil {
		return err
	}
	return records.Append(name, line, 0o644)
}

func eventLine(now time.Time, e Event) ([]byte, error) {
	e.At = now.Format(time.RFC3339)
	e.Text = e.text()
	e.After = ""
	data, err := MarshalEvent(e)
	return append(data, '\n'), err
}

// ptr gives the optional fields that are written even when empty.
func ptr[T any](v T) *T { return &v }

// handover keeps the body of task.md as it was when the work changed hands.
func handover(from, to Status, body string) Event {
	if len(body) > MaxHandoverBody {
		cut := MaxHandoverBody
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut]
	}
	return Event{Type: EventHandover, From: string(from), To: string(to), Body: body}
}

// Line is one event read back from log.jsonl. Raw is the line as stored;
// Event is empty when the line is not JSON.
type Line struct {
	Raw   string
	Event Event
	OK    bool
}

// ReadEvents reads a log.jsonl. A line that is not JSON is kept with OK
// false, so that nothing written is hidden.
func ReadEvents(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Line
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var e Event
		err := json.Unmarshal([]byte(raw), &e)
		out = append(out, Line{Raw: raw, Event: e, OK: err == nil})
	}
	return out, scanner.Err()
}

// Format writes an event the way log.md showed it: "- 2026-11-24 09:15
// text（by）". An event moved from log.md already names its writer in the
// text, and a block of log.md that had no shape is shown as it was.
func (l Line) Format() string {
	return l.FormatIn(JST)
}

// FormatIn displays the event in the zone of the owning repository.
func (l Line) FormatIn(loc *time.Location) string {
	e := l.Event
	if !l.OK {
		return l.Raw
	}
	if e.Type == EventLegacy {
		return e.Text
	}
	text := e.Text
	switch {
	case e.Type == EventHandover && e.Handover == "":
		text = "このときの本文を残しました。"
	case text == "":
		return l.Raw
	}
	if e.By != "" && !e.Imported {
		text += "（" + e.By + "）"
	}
	at, err := time.Parse(time.RFC3339, e.At)
	if err != nil {
		return l.Raw
	}
	return "- " + at.In(loc).Format("2006-01-02 15:04") + " " + text
}

// HistoryLines returns the history of a task as lines a person reads: the
// events of log.jsonl formatted as dated text. tail
// keeps the last ones only, when positive.
func (s *Store) HistoryLines(t Task, tail int) ([]string, error) {
	return ReadHistoryIn(filepath.Join(s.root, t.Dir), tail, s.Location())
}

// ReadHistory reads the history in dir, a task or project directory.
func ReadHistory(dir string, tail int) ([]string, error) {
	return ReadHistoryIn(dir, tail, JST)
}

// ReadHistoryIn renders the history in the configured local time.
func ReadHistoryIn(dir string, tail int, loc *time.Location) ([]string, error) {
	events, err := ReadEvents(filepath.Join(dir, HistoryFile))
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, e := range events {
		lines = append(lines, e.FormatIn(loc))
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}
