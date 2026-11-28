// Package daylog keeps what was said in one Markdown file per day inside
// the task repository: the conversation on Discord on the chat side, and
// the notes Lacia jots down in either mode. The files are only ever
// appended to, so they can be searched later and never need merging.
package daylog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
)

// Defaults for Log. Both can be changed where the server is started.
const (
	DefaultEntryLimit = 4000    // characters of one message kept in the log
	DefaultDayLimit   = 200_000 // bytes of one day's file before bodies are left out
)

// jst is the default zone for the log's dates and times.
var jst = time.FixedZone("JST", 9*60*60)

// File is what the log keeps about an attachment. The content is never
// stored, and neither is Discord's URL, which stops working after a while.
type File struct {
	Name string
	Type string // MIME type, when Discord told it
	Size int64  // bytes; 0 when unknown
}

// Entry is one thing the log keeps: something a family member wrote,
// something Lacia posted, or a note she jotted down.
type Entry struct {
	// Key identifies the entry, e.g. "message:<Discord ID>" for what was
	// heard, "post:<request ID>" for what was posted and "notice:<time>"
	// for a note. An entry whose key
	// is already in the day's file is not written again. More lists further
	// keys the entry also stands for (messages folded into one request).
	Key   string
	More  []string
	At    time.Time
	Who   string // how people there call the author, "レイシア", or what kind of note it is
	Place string // e.g. "#lacia", or "#lacia › 旅行の相談" for a thread; may be empty
	Text  string
	Files []File
	// Remark is a line from the server about the entry, written outside the
	// quoted text.
	Remark string
}

// Log appends entries to one Markdown file per day, in a folder per month:
// <Dir>/2026-11/2026-11-18.md inside the records. It only ever appends,
// with a single write per entry, so a second process can add to the same
// files. Saving to Git is left to whoever watches the
// repository; Changed tells them.
type Log struct {
	Location   *time.Location
	Records    *disk.Dir
	Dir        string // relative to Records
	Title      string // what a day's file is called, e.g. "Discordのやり取り"
	About      string // the paragraph under the title of each new file
	EntryLimit int    // 0 means DefaultEntryLimit
	DayLimit   int    // 0 means DefaultDayLimit
	Changed    func() // called after something was written
	DryRun     bool   // report what would be written, and write nothing

	mu sync.Mutex
}

// Path is the file an entry written at t belongs to.
func (l *Log) Path(t time.Time) string { return filepath.Join(l.Records.Path(), l.name(t)) }

func (l *Log) name(t time.Time) string {
	t = t.In(l.Zone())
	return filepath.Join(l.Dir, t.Format("2006-01"), t.Format("2006-01-02")+".md")
}

// Add writes e at the end of its day's file and reports whether it did. It
// does nothing when the key is already there, so receiving a message twice
// leaves one entry.
func (l *Log) Add(e Entry) (bool, error) {
	if l == nil || l.Records == nil {
		return false, nil
	}
	if e.Key == "" || strings.ContainsAny(e.Key, " \t\r\n>") || e.At.IsZero() {
		return false, errors.New("ログへ書く発言に、識別子か時刻がありません")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e.At = e.At.In(l.Zone())
	name := l.name(e.At)
	existing, err := l.Records.ReadFile(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if hasKey(existing, e.Key) {
		return false, nil
	}
	if l.DryRun {
		return true, nil
	}
	entryLimit, dayLimit := l.EntryLimit, l.DayLimit
	if entryLimit <= 0 {
		entryLimit = DefaultEntryLimit
	}
	if dayLimit <= 0 {
		dayLimit = DefaultDayLimit
	}
	var out bytes.Buffer
	if len(existing) == 0 {
		day := e.At.In(l.Zone())
		fmt.Fprintf(&out, "# %s（%s）の%s\n", day.Format("2006-01-02"), weekdays[day.Weekday()], l.Title)
		if l.About != "" {
			fmt.Fprintf(&out, "\n%s時刻は %s で表示します。`>` で始まる行が本文です。このファイルは手で書き換えないでください。\n", l.About, l.Zone())
		}
	}
	head := out.Len()
	out.WriteString(render(e, entryLimit))
	if len(existing)+out.Len() > dayLimit {
		// The day is full: keep who spoke and when, so the count can still
		// be told, and leave the text out.
		out.Truncate(head)
		out.WriteString(heading(e) + "\n（1日分の大きさの上限を超えたので、本文は書いていません。）\n")
	}
	if err := l.Records.Append(name, out.Bytes(), 0o644); err != nil {
		return false, err
	}
	if l.Changed != nil {
		l.Changed()
	}
	return true, nil
}

var weekdays = [...]string{"日", "月", "火", "水", "木", "金", "土"}

const (
	markOpen  = "<!-- "
	markClose = " -->"
)

// hasKey looks for key in the headings of a day's file. Only headings are
// read: the text of a message is quoted, so nothing written on Discord can
// pass for one.
func hasKey(file []byte, key string) bool {
	sc := bufio.NewScanner(bytes.NewReader(file))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "## ") || !strings.HasSuffix(line, markClose) {
			continue
		}
		i := strings.LastIndex(line, markOpen)
		if i < 0 {
			continue
		}
		for _, k := range strings.Fields(line[i+len(markOpen) : len(line)-len(markClose)]) {
			if k == key {
				return true
			}
		}
	}
	return false
}

// oneLine keeps a name from breaking the heading it is written in.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("<!--", "", "-->", "", "（", "(", "）", ")").Replace(s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	if s == "" {
		s = "（不明）"
	}
	return s
}

func heading(e Entry) string {
	keys := append([]string{e.Key}, e.More...)
	who := oneLine(e.Who)
	if e.Place != "" {
		who += "（" + oneLine(e.Place) + "）"
	}
	return fmt.Sprintf("\n## %s %s %s%s%s", e.At.Format("15:04"), who, markOpen, strings.Join(keys, " "), markClose)
}

func render(e Entry, limit int) string {
	var b strings.Builder
	b.WriteString(heading(e) + "\n")
	text := strings.TrimSpace(strings.ReplaceAll(e.Text, "\r\n", "\n"))
	cut := 0
	if r := []rune(text); len(r) > limit {
		cut, text = len(r), strings.TrimSpace(string(r[:limit]))
	}
	if text != "" {
		b.WriteString("\n")
		for _, line := range strings.Split(text, "\n") {
			b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
		}
	}
	var notes []string
	if cut > 0 {
		notes = append(notes, fmt.Sprintf("（長いので、%d字で切りました。もとは%d字です。）", limit, cut))
	}
	for _, f := range e.Files {
		notes = append(notes, "添付ファイル: "+oneLine(f.Name)+fileDetail(f))
	}
	if e.Remark != "" {
		notes = append(notes, "（"+strings.Join(strings.Fields(e.Remark), " ")+"）")
	}
	if len(notes) > 0 {
		b.WriteString("\n" + strings.Join(notes, "\n") + "\n")
	}
	return b.String()
}

func fileDetail(f File) string {
	var parts []string
	if f.Type != "" {
		parts = append(parts, strings.Join(strings.Fields(f.Type), ""))
	}
	switch {
	case f.Size >= 1<<20:
		parts = append(parts, fmt.Sprintf("%.1f MiB", float64(f.Size)/(1<<20)))
	case f.Size >= 1<<10:
		parts = append(parts, fmt.Sprintf("%d KiB", f.Size>>10))
	case f.Size > 0:
		parts = append(parts, fmt.Sprintf("%dバイト", f.Size))
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "、") + "）"
}

// Discord is the log of the conversation on Discord, kept under dir.
func Discord(records *disk.Dir, dir string) *Log {
	return &Log{Records: records, Dir: dir, Title: "Discordのやり取り",
		About: "Discord で受け取った発言とわたしの投稿を、サーバーが元の言葉で記録します。書き留めもこのログに残す設定なら、同じ日のファイルに入ります。"}
}

// Notices is the log of Lacia's notes alone, for a profile without Discord.
func Notices(records *disk.Dir, dir string) *Log {
	return &Log{Records: records, Dir: dir, Title: "書き留め",
		About: "サーバーは、わたしの書き留め（`lacia notice`）と、「わたしのこと」について伝えていただいた言葉を、元の文面のまま記録します。"}
}

// Notice is the entry for a note. from is empty when Lacia wrote it
// herself, and otherwise names the person whose words it is.
func Notice(at time.Time, from, taskID, text string) Entry {
	e := Entry{Key: "notice:" + strconv.FormatInt(at.UnixNano(), 10), At: at, Who: "レイシアの書き留め", Text: text}
	if from != "" {
		e.Who = from + "が「わたしのこと」について伝えた言葉"
	}
	if taskID != "" {
		e.Place = "タスク " + taskID
	}
	return e
}

// Zone returns the configured calendar zone for day boundaries.
func (l *Log) Zone() *time.Location {
	if l.Location != nil {
		return l.Location
	}
	return jst
}
