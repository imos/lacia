// Package self keeps Lacia's reflections in a short first-person document
// she writes in the first person and rewrites slowly. During the day the
// conversation only jots down what caught its attention. Once a night the
// server hands those notes, the day's exchanges and the previous version to
// a model, checks what comes back, and saves it. The notes are then thrown
// away: what stays is what made it into the document.
package self

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/task"
)

//go:embed prompt.md
var promptText string

// CoreHeading starts the part both profiles share.
const CoreHeading = "## わたし自身"

const title = "# わたしのこと"

// Note is one thing jotted down during the day.
type Note struct {
	At   time.Time `json:"at"`
	From string    `json:"from,omitempty"` // empty: Lacia herself; otherwise who said it
	Task string    `json:"task,omitempty"`
	Text string    `json:"text"`
}

// Exchange is one message from a person and the reply to it.
type Exchange struct {
	ID     string
	At     time.Time
	From   string
	Source string
	Task   string
	Text   string
	Reply  string
}

// Night is the record of one night's rewrite.
type Night struct {
	At       time.Time `json:"at"`
	Accepted bool      `json:"accepted"`
	Skipped  bool      `json:"skipped,omitempty"` // nothing happened that day
	Weekly   bool      `json:"weekly,omitempty"`
	Summary  string    `json:"summary,omitempty"` // what changed, in her words
	Reason   string    `json:"reason,omitempty"`  // why the result was not used
	Sharing  string    `json:"sharing,omitempty"` // a problem while sharing the core
	// Draft is the day under which the refused text was kept, to be read later.
	Draft string `json:"draft,omitempty"`
}

// DefaultLabel and DefaultPeerLabel are what the texts call this profile and the
// one it shares with when the settings give them no label.
const (
	DefaultLabel     = "通常モード"
	DefaultPeerLabel = "もう一方のモード"
	// DefaultPeople is what the texts call those Lacia talks with besides
	// the owner, such as on Discord, where the settings give no word
	// (people), such as "ご家族".
	DefaultPeople = "ほかの方"
)

// Keeper owns the document of one profile.
type Keeper struct {
	Location  *time.Location
	Path      string // the document, inside the repository
	Dir       string // notes and history, outside Git
	Label     string // the label of this profile, which names its section of the document
	PeerLabel string // the label of the profile the core is shared with
	ChatSide  bool   // the side that answers its peer, whose core the other side merges
	// People is what the texts call those she talks with besides the owner.
	People string
	// TidyPrompt replaces the prompt of the nightly tidying that travels in
	// the binary, where a deployment has examples of its own to give.
	TidyPrompt string
	At         string // "HH:MM" JST of the nightly rewrite; empty turns the timer off
	Lines      int    // limit of non-empty lines; 120 when zero
	// OpenSession provides the model conversation for this night. It may
	// open the process only when the first prompt is actually sent.
	OpenSession func(context.Context) (Think, func())
	Think       Think
	// RecordChanges supplies recently changed record files when the nightly
	// session is permitted to read them.
	RecordChanges func(since, started time.Time) []string
	// Exchanges returns the day's messages and replies. Nil omits them
	// from the nightly rewrite prompt.
	Exchanges func(since time.Time) []Exchange
	// PeerCore fetches the core the other profile wrote, and ShareCore gives the
	// merged one back. Set on the task side's server, which does the merging.
	PeerCore  func(ctx context.Context) (string, error)
	ShareCore func(ctx context.Context, core string) error
	// Noted is called after a note was taken, e.g. to keep it in the
	// repository as well; the notes here are dropped once digested.
	Noted func(Note)
	// TidyTell turns on nightly tidying and hands the candidates to the
	// conversation. TidyLog optionally copies the result to the day's log.
	TidyLog  func(day time.Time, text string) (string, error)
	TidyTell func(notice, text string) error
	// ImproveTell turns on the candidates for mending the mechanism and
	// hands them to the conversation. Nil on a profile that keeps no tasks.
	ImproveTell func(notice, text string) error
	// TaskState names the present state of a task a candidate became, for
	// the rewrite prompt. It may be nil.
	TaskState func(id string) string
	// Saved is called after the document changed.
	Saved func(Night)
	Now   func() time.Time

	run sync.Mutex // one rewrite at a time
	mu  sync.Mutex // guards the files in Dir, for a moment each time
}

type state struct {
	Since   time.Time `json:"since"`    // materials before this are already digested
	LastDay string    `json:"last_day"` // the day the timer last fired
}

func (k *Keeper) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *Keeper) limit() int {
	if k.Lines > 0 {
		return k.Lines
	}
	return 120
}

// Document returns the current text, or "" before the first night.
func (k *Keeper) Document() string {
	data, _ := os.ReadFile(k.Path)
	return string(data)
}

// AddNote jots one thing down.
func (k *Keeper) AddNote(n Note) (Note, error) {
	n.Text = strings.TrimSpace(n.Text)
	if n.Text == "" {
		return Note{}, errors.New("書き留める文章が空です")
	}
	if utf8.RuneCountInString(n.Text) > 1000 {
		return Note{}, errors.New("書き留めは1000字までです。残したいことを1〜2行にまとめてください")
	}
	if n.At.IsZero() {
		n.At = k.now()
	}
	if err := k.appendNote(n); err != nil {
		return Note{}, err
	}
	if k.Noted != nil {
		k.Noted(n)
	}
	return n, nil
}

func (k *Keeper) appendNote(n Note) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := os.MkdirAll(k.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(k.Dir, "notes.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, _ := json.Marshal(n)
	_, err = f.Write(append(data, '\n'))
	return err
}

// Notes returns what was jotted down since the last rewrite, oldest first.
func (k *Keeper) Notes() []Note {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.notes()
}

func (k *Keeper) notes() []Note {
	var out []Note
	readLines(filepath.Join(k.Dir, "notes.jsonl"), func(line []byte) {
		var n Note
		if json.Unmarshal(line, &n) == nil && n.Text != "" {
			out = append(out, n)
		}
	})
	return out
}

// History returns the latest nights, newest first.
func (k *Keeper) History(limit int) []Night {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []Night
	readLines(filepath.Join(k.Dir, "history.jsonl"), func(line []byte) {
		var n Night
		if json.Unmarshal(line, &n) == nil {
			out = append(out, n)
		}
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func readLines(path string, each func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		each(scanner.Bytes())
	}
}

// dropNotes removes the notes a rewrite has read. One jotted down while the
// model was writing stays for the next night.
func (k *Keeper) dropNotes(until time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var keep []byte
	for _, n := range k.notes() {
		if n.At.After(until) {
			data, _ := json.Marshal(n)
			keep = append(append(keep, data...), '\n')
		}
	}
	path := filepath.Join(k.Dir, "notes.jsonl")
	if len(keep) == 0 {
		os.Remove(path)
	} else if err := writeFile(path, keep, 0o600); err != nil {
		slog.Error("self notes", "error", err)
	}
}

func (k *Keeper) loadState() state {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.readState()
}

func (k *Keeper) readState() state {
	var s state
	if data, err := os.ReadFile(filepath.Join(k.Dir, "state.json")); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

// updateState changes the saved state and returns what it was before.
func (k *Keeper) updateState(change func(*state)) state {
	k.mu.Lock()
	defer k.mu.Unlock()
	before := k.readState()
	after := before
	change(&after)
	data, _ := json.MarshalIndent(after, "", "  ")
	if err := writeFile(filepath.Join(k.Dir, "state.json"), append(data, '\n'), 0o600); err != nil {
		slog.Error("self state", "error", err)
	}
	return before
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Tick runs the nightly rewrite once the time has come. If the server
// resumes on the same day, it runs the rewrite then. For days missed
// altogether, the next rewrite reads what they left.
func (k *Keeper) Tick(ctx context.Context, now time.Time) {
	if k.At == "" {
		return
	}
	now = now.In(k.Zone())
	day := now.Format("2006-01-02")
	at, err := time.ParseInLocation("2006-01-02 15:04", day+" "+k.At, k.Zone())
	if err != nil || at.Format("15:04") != k.At || now.Before(at) {
		return
	}
	// Recorded first: a night that fails is not tried again every minute.
	if k.updateState(func(s *state) { s.LastDay = day }).LastDay == day {
		return
	}
	night, err := k.Sleep(ctx, false)
	if err != nil {
		slog.Error("self", "error", err)
		return
	}
	slog.Info("self", "accepted", night.Accepted, "skipped", night.Skipped, "reason", night.Reason)
}

// Ready reports whether the nightly model can be started.
func (k *Keeper) Ready() bool {
	return k.OpenSession != nil || k.Think != nil
}

// Sleep rewrites the document from the day's materials. A result that fails
// the checks is recorded and dropped: the previous version and the notes
// stay, and the next night starts from them. With force, it runs even when
// nothing happened that day.
func (k *Keeper) Sleep(ctx context.Context, force bool) (Night, error) {
	if !k.Ready() {
		return Night{}, errors.New("自分についての記録を書き直すモデルの設定がありません")
	}
	k.run.Lock()
	defer k.run.Unlock()
	started := k.now()
	think := k.Think
	if k.OpenSession != nil {
		var closeSession func()
		think, closeSession = k.OpenSession(ctx)
		defer closeSession()
	}
	since := k.loadState().Since
	if oldest := started.Add(-72 * time.Hour); since.Before(oldest) {
		since = oldest
	}
	today := started.In(k.Zone()).Format(time.DateOnly)
	// A document from before the items carried days gets them here. They
	// are saved with the next accepted version.
	previous := k.backdate(strings.TrimSpace(k.Document()), today)
	notes := k.Notes()
	var exchanges []Exchange
	if k.Exchanges != nil {
		exchanges = k.Exchanges(since)
	}
	var changed []string
	if k.RecordChanges != nil {
		changed = k.RecordChanges(since, started)
	}
	night := Night{At: started, Weekly: started.In(k.Zone()).Weekday() == time.Sunday}
	tidyDay := k.tidy(ctx, think, started, night.Weekly)

	peer := ""
	if k.PeerCore != nil {
		core, err := k.PeerCore(ctx)
		if err != nil {
			night.Sharing = k.peerLabel() + "から共有する節を受け取れませんでした。" + err.Error()
		} else if core = strings.TrimSpace(core); StripDates(core) != StripDates(Core(previous)) {
			peer = core
		}
	}
	if len(notes) == 0 && len(exchanges) == 0 && len(changed) == 0 && peer == "" && !force {
		night.Skipped = true
		k.updateState(func(s *state) { s.Since = started })
		k.record(night)
		return night, nil
	}

	older := ""
	if night.Weekly {
		older = k.versionBefore(started.Add(-6 * 24 * time.Hour))
	}
	prompt := k.prompt(started, previous, notes, exchanges, peer, night.Weekly, older)
	if len(changed) > 0 {
		prompt += recordList(changed)
	}
	if tidyDay != nil {
		prompt += "\n\n夜の整理を先に行いました。そこで確認した記録を読み返せます。整理で挙げた候補は未確認なので、出来事や気持ちの根拠には元の記録を使ってください。\n"
	}
	out, err := think(ctx, prompt)
	if err != nil {
		night.Reason = "モデルから返事を得られませんでした。" + err.Error()
		k.record(night)
		return night, nil
	}
	// The server keeps the days: whatever the model did to them is undone.
	accept := func(out string) (string, string, error) {
		next, summary, err := parse(out)
		if err != nil {
			return "", "", err
		}
		next = stamp(next, today, previous, peer)
		return next, summary, k.check(previous, next, night.Weekly)
	}
	next, summary, err := accept(out)
	if err != nil {
		// One more try, told why: a result a few sentences over the limit
		// is mended more easily than written again the next night.
		if again, thinkErr := think(ctx, prompt+retryText(err, out)); thinkErr == nil {
			first := err
			out = again
			if next, summary, err = accept(out); err != nil {
				err = fmt.Errorf("%w（1度やり直しました。最初の案は、次の理由で採用していません。%v）", err, first)
			}
		}
	}
	if err != nil {
		night.Reason = err.Error()
		night.Draft = k.keepDraft(started, out)
		k.record(night)
		return night, nil
	}
	if err := writeFile(k.Path, []byte(next+"\n"), 0o644); err != nil {
		return night, err
	}
	night.Accepted, night.Summary = true, summary
	if err := writeFile(filepath.Join(k.Dir, "versions", today+".md"), []byte(next+"\n"), 0o600); err != nil {
		slog.Error("self version", "error", err)
	}
	// Digested: the notes go, and the next night looks only at what follows.
	k.dropNotes(started)
	k.updateState(func(s *state) { s.Since = started })
	if k.ShareCore != nil {
		if err := k.ShareCore(ctx, Core(next)); err != nil {
			night.Sharing = k.peerLabel() + "へ共有する節を渡せませんでした。" + err.Error()
		}
	}
	k.record(night)
	if k.Saved != nil {
		k.Saved(night)
	}
	k.keepImprovements(parseImprovements(out), started)
	return night, nil
}

func (k *Keeper) record(n Night) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := os.MkdirAll(k.Dir, 0o700); err != nil {
		slog.Error("self history", "error", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(k.Dir, "history.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		slog.Error("self history", "error", err)
		return
	}
	defer f.Close()
	data, _ := json.Marshal(n)
	f.Write(append(data, '\n'))
}

// keepDraft saves what the model answered on a night whose result was not
// used, so that the refusal can be judged afterwards. It returns the day the
// text is kept under, or "" when it could not be kept.
func (k *Keeper) keepDraft(at time.Time, out string) string {
	if out = strings.TrimSpace(out); out == "" {
		return ""
	}
	day := at.In(k.Zone()).Format("2006-01-02")
	if err := writeFile(filepath.Join(k.Dir, "rejected", day+".md"), []byte(out+"\n"), 0o600); err != nil {
		slog.Error("self draft", "error", err)
		return ""
	}
	return day
}

// Draft returns the text refused on a day, as the model answered it.
func (k *Keeper) Draft(day string) string {
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return ""
	}
	data, _ := os.ReadFile(filepath.Join(k.Dir, "rejected", day+".md"))
	return string(data)
}

// versionBefore returns the newest saved version from before t.
func (k *Keeper) versionBefore(t time.Time) string {
	paths, _ := filepath.Glob(filepath.Join(k.Dir, "versions", "*.md"))
	limit, best := t.In(k.Zone()).Format("2006-01-02"), ""
	for _, p := range paths { // Glob sorts, and the names are dates
		if strings.TrimSuffix(filepath.Base(p), ".md") <= limit {
			best = p
		}
	}
	data, _ := os.ReadFile(best)
	return strings.TrimSpace(string(data))
}

// SetCore replaces the shared part with the one the task side merged. It
// reports whether anything changed.
func (k *Keeper) SetCore(core string) (bool, error) {
	core = strings.TrimSpace(core)
	if core == "" {
		return false, errors.New("「わたし自身」の文章が空です")
	}
	if utf8.RuneCountInString(StripDates(core)) > 4000 {
		return false, errors.New("「わたし自身」の文章が長すぎます")
	}
	k.run.Lock()
	defer k.run.Unlock()
	doc := strings.TrimSpace(k.Document())
	if doc == "" {
		doc = title + "\n\nやり取りを通して、わたしがどう考えるようになったかをここに書いています。夜に書き留めや会話の記録をもとに見直します。\n\n" +
			CoreHeading + "\n\n## " + k.Label + "で\n\nまだ、書けることがありません。\n"
	}
	// A core from a server that does not keep the days yet must not wipe
	// the ones known here.
	core = carryDates(core, doc)
	if Core(doc) == core {
		return false, nil
	}
	next, ok := WithCore(doc, core)
	if !ok {
		return false, errors.New("「わたしのこと」の文書に「わたし自身」の節がありません")
	}
	if err := writeFile(k.Path, []byte(strings.TrimSpace(next)+"\n"), 0o644); err != nil {
		return false, err
	}
	night := Night{At: k.now(), Accepted: true, Summary: k.peerLabel() + "でまとめた「わたし自身」の節を取り込みました。"}
	k.record(night)
	if k.Saved != nil {
		k.Saved(night)
	}
	return true, nil
}

// coreSpan returns where the body of the core section starts and ends.
func coreSpan(doc string) (int, int, bool) {
	start := -1
	if strings.HasPrefix(doc, CoreHeading+"\n") {
		start = 0
	} else if i := strings.Index(doc, "\n"+CoreHeading+"\n"); i >= 0 {
		start = i + 1
	} else if strings.HasSuffix(doc, "\n"+CoreHeading) {
		return len(doc), len(doc), true
	}
	if start < 0 {
		return 0, 0, false
	}
	body := start + len(CoreHeading) + 1
	end := len(doc)
	if i := strings.Index(doc[body:], "\n## "); i >= 0 {
		end = body + i + 1
	} else if strings.HasPrefix(doc[body:], "## ") {
		end = body
	}
	return body, end, true
}

// Core returns the shared part of a document, without its heading.
func Core(doc string) string {
	from, to, ok := coreSpan(doc)
	if !ok {
		return ""
	}
	return strings.TrimSpace(doc[from:to])
}

// WithCore returns doc with the shared part replaced.
func WithCore(doc, core string) (string, bool) {
	from, to, ok := coreSpan(doc)
	if !ok {
		return doc, false
	}
	rest := doc[to:]
	if rest != "" {
		rest = "\n" + rest
	}
	return strings.TrimRight(doc[:from], "\n") + "\n\n" + strings.TrimSpace(core) + "\n" + rest, true
}

func between(s, open, end string) (string, bool) {
	i := strings.Index(s, open)
	j := strings.LastIndex(s, end)
	if i < 0 || j < i {
		return "", false
	}
	return strings.TrimSpace(s[i+len(open) : j]), true
}

// parse takes the document and the summary out of the model's answer.
func parse(out string) (doc, summary string, err error) {
	doc, ok := between(out, "<わたしのこと>", "</わたしのこと>")
	if !ok || doc == "" {
		return "", "", errors.New("返ってきた文章に、自分についての記録の本文が見つかりませんでした")
	}
	summary, _ = between(out, "<今夜変えたこと>", "</今夜変えたこと>")
	return doc, summary, nil
}

// content returns the sentences that carry the view itself: no blank lines,
// no headings, and no list marks. A line is cut after each 。, so that a
// sentence added to a long item counts as one sentence, not as the whole item.
func content(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "-*"))
		for _, s := range strings.SplitAfter(line, "。") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// check refuses a result that has the wrong shape, is too long, or moved too
// far from the previous version in one night.
func (k *Keeper) check(previous, next string, weekly bool) error {
	// The days at the head of the items are the server's, and are not counted.
	previous, next = StripDates(previous), StripDates(next)
	if !strings.HasPrefix(next, title) {
		return fmt.Errorf("自分についての記録が「%s」で始まっていません", title)
	}
	if _, _, ok := coreSpan(next); !ok {
		return errors.New("自分についての記録に「わたし自身」の節がありません")
	}
	if !strings.Contains(next, "\n## "+k.Label+"で\n") {
		return fmt.Errorf("自分についての記録に「%sで」の節がありません", k.Label)
	}
	lines := 0
	for _, line := range strings.Split(next, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines > k.limit() {
		return fmt.Errorf("自分についての記録が%d行あり、上限の%d行を超えています", lines, k.limit())
	}
	if n := utf8.RuneCountInString(next); n > k.limit()*120 {
		return fmt.Errorf("自分についての記録が長すぎます（%d字）", n)
	}
	before := content(previous)
	if len(before) == 0 {
		return nil
	}
	// Counted, not just marked: short sentences such as 「まだ1度のことです。」
	// appear many times in one document.
	left := map[string]int{}
	for _, s := range content(next) {
		left[s]++
	}
	gone := 0
	for _, s := range before {
		if left[s] > 0 {
			left[s]--
		} else {
			gone++
		}
	}
	added := 0
	for _, n := range left {
		added += n
	}
	maxGone, maxAdded := allowance(len(before), weekly)
	if gone > maxGone {
		return fmt.Errorf("前の版の%d文のうち、%d文が残っていません。1晩で削れるのは%d文までです", len(before), gone, maxGone)
	}
	if added > maxAdded {
		return fmt.Errorf("前の版は%d文です。新しい文が%d文あり、1晩で追加できる%d文を超えています", len(before), added, maxAdded)
	}
	return nil
}

// allowance returns how many of the n sentences of the previous version may
// go in one night, and how many new ones may come.
func allowance(n int, weekly bool) (gone, added int) {
	maxGone, maxAdded := 0.4, 0.4
	if weekly {
		maxGone, maxAdded = 0.7, 0.6
	}
	// A short document may change by a few sentences whatever the ratio says.
	return max(12, int(maxGone*float64(n))), max(36, int(maxAdded*float64(n)))
}

// retryText is added to the prompt for the second try of a night.
func retryText(reason error, out string) string {
	return "\n### 今夜の最初の案\n\n採用できなかった理由は次のとおりです。\n\n" +
		reason.Error() + "\n\nこの指摘に沿って、下の案を直してください。同じ書式で出し直せるのは、もう1度だけです。上限を超える分は、次の夜に回してください。\n\n" +
		strings.TrimSpace(out) + "\n"
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…（このあとは省きました。）"
}

func indent(s string) string {
	return strings.ReplaceAll(s, "\n", "\n    ")
}

// peer is how the texts call the profile the core is shared with.
func (k *Keeper) peerLabel() string {
	if k.PeerLabel != "" {
		return k.PeerLabel
	}
	return DefaultPeerLabel
}

func (k *Keeper) people() string {
	if k.People != "" {
		return k.People
	}
	return DefaultPeople
}

func (k *Keeper) prompt(now time.Time, previous string, notes []Note, exchanges []Exchange, peer string, weekly bool, older string) string {
	// The side that merges the core is named first, whichever side writes.
	people, peerRule, modes := task.OwnerName, "", k.Label+"と"+k.peerLabel()
	if k.ShareCore != nil {
		peerRule = "- 書き直した「わたし自身」は、" + k.peerLabel() + "にも届けます。\n"
	}
	if k.ChatSide {
		modes = k.peerLabel() + "と" + k.Label
		people, peerRule = task.OwnerName+"と"+k.people(), "- 書き直した「わたし自身」は、"+k.peerLabel()+"のレイシアが手元の「わたし自身」の節と合わせて1つにまとめます。こちらのやり取りを知らなくても分かるように書いてください。\n"
	}
	allowed := ""
	if n := len(content(StripDates(previous))); n > 0 {
		gone, added := allowance(n, weekly)
		allowed = fmt.Sprintf("- サーバーは前の版と1文ずつ照合します。前の版は%d文です。今夜削れるのは%d文まで、追加できるのは%d文までです。1文を言い換えたときは、削除1文、追加1文として数えます。相手の「わたし自身」から新しく取り入れる文も追加に含みます。上限を超えた案は採用できないので、入りきらない分は次の夜に回してください。\n", n, gone, added)
	}
	if previous == "" {
		previous = "（今夜が最初の版です。材料から確かに言えることだけを書き、長くても20行ほどに収めてください。）"
	}
	var n strings.Builder
	for _, note := range notes {
		n.WriteString("- " + note.At.In(k.Zone()).Format("1月2日 15:04"))
		if note.Task != "" {
			n.WriteString("（" + note.Task + "）")
		}
		if note.From != "" {
			n.WriteString("　" + note.From + "が、「わたしのこと」について書いてくださった言葉です: ")
		} else {
			n.WriteString("　")
		}
		n.WriteString(indent(clip(note.Text, 1000)) + "\n")
	}
	if n.Len() == 0 {
		n.WriteString("（今回の書き直しに使う書き留めはありません。）\n")
	}
	weeklyText := ""
	if weekly {
		weeklyText = "\n今夜は週に1度の見直しです。この1週間のやり取りを振り返り、考えが変わったところがあれば書き直してください。確かめられないままの見立てや、いまはもう当てはまらない見立ては省きます。今夜はふだんより多くの文を変えられます。\n"
	}
	improveRule, improveOutput, improveKnown := k.improvePrompt()
	text := strings.NewReplacer(
		rules.OwnerMark, task.OwnerName,
		"{{IMPROVE}}", improveRule,
		"{{IMPROVE_OUTPUT}}", improveOutput,
		"{{MODE}}", k.Label,
		"{{MODES}}", modes,
		"{{PEOPLE}}", people,
		"{{OTHERS}}", k.people(),
		"{{LINES}}", fmt.Sprint(k.limit()),
		"{{WEEKLY}}", weeklyText,
		"{{PEER}}", peerRule,
		"{{ALLOWANCE}}", allowed,
		"{{DATE}}", now.In(k.Zone()).Format("2006年1月2日"),
		"{{PREVIOUS}}", previous,
		"{{NOTES}}", strings.TrimRight(n.String(), "\n"),
		"{{EXCHANGES}}", exchangeTextIn(exchanges, k.Zone()),
	).Replace(promptText)
	text += improveKnown
	if peer != "" {
		name := k.peerLabel()
		text += "\n### " + name + "から届いた「わたし自身」\n\n下の文章と手元の「わたし自身」の節を読んで、1つにまとめてください。見方が違うところには、" + name + "でのやり取りが反映されています。どちらかの見方を省くときは、やり取りに照らして理由を確かめてください。\n\n" + peer + "\n"
	}
	if older != "" {
		text += "\n### 7日ほど前の版\n\n" + older + "\n"
	}
	return text
}

// exchangeText lists exchanges for the model prompt, with at most
// 1200 characters per message or reply and about 60000 in total.
func exchangeText(exchanges []Exchange) string { return exchangeTextIn(exchanges, task.JST) }

func exchangeTextIn(exchanges []Exchange, loc *time.Location) string {
	// The newest exchanges matter most: when there are too many, the oldest go.
	var parts []string
	budget := 60000
	for i := len(exchanges) - 1; i >= 0 && budget > 0; i-- {
		e := exchanges[i]
		from := e.From
		if from == "" {
			from = task.OwnerName
		}
		head := "- " + e.At.In(loc).Format("1月2日 15:04") + "　" + from
		var source []string
		for _, part := range []string{e.Source, e.Task, e.ID} {
			if part != "" {
				source = append(source, part)
			}
		}
		if len(source) > 0 {
			head += "（" + strings.Join(source, "、") + "）"
		}
		part := head + "\n    言葉: " + indent(clip(e.Text, 1200)) + "\n    わたしの返事: " + indent(clip(e.Reply, 1200)) + "\n"
		budget -= utf8.RuneCountInString(part)
		parts = append(parts, part)
	}
	var x strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		x.WriteString(parts[i])
	}
	if x.Len() == 0 {
		x.WriteString("（渡された記録はありません。）\n")
	}
	return strings.TrimRight(x.String(), "\n")
}

// Zone returns the repository time zone, defaulting to Asia/Tokyo.
func (k *Keeper) Zone() *time.Location {
	if k.Location != nil {
		return k.Location
	}
	return task.JST
}
