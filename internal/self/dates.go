package self

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// An item of the document is one list line. It starts with the day its
// text last changed, as "- [2026-11-24] ...". The server writes that day;
// the model only reads it.
var itemLine = regexp.MustCompile(`^([-*] )(?:\[(\d{4}-\d{2}-\d{2})\] ?)?(.*)$`)

// splitItem takes a list line apart. ok is false for any other line.
func splitItem(line string) (mark, day, body string, ok bool) {
	m := itemLine.FindStringSubmatch(line)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], strings.TrimSpace(m[3]), true
}

// StripDates returns doc without the days at the head of its items. The
// checks of length and of change count the text alone.
func StripDates(doc string) string {
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if mark, day, body, ok := splitItem(line); ok && day != "" {
			lines[i] = mark + body
		}
	}
	return strings.Join(lines, "\n")
}

// itemDates maps the text of each dated item to its day. Where the same
// text appears twice, the older day stays.
func itemDates(doc string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(doc, "\n") {
		if _, day, body, ok := splitItem(line); ok && day != "" {
			if old, seen := out[body]; !seen || day < old {
				out[body] = day
			}
		}
	}
	return out
}

// undated reports whether doc has an item without a day.
func undated(doc string) bool {
	for _, line := range strings.Split(doc, "\n") {
		if _, day, _, ok := splitItem(line); ok && day == "" {
			return true
		}
	}
	return false
}

// stamp writes a day on every item of doc: the one kept for the same text
// in any of the sources, the older where several know it, and otherwise
// today. Whatever day the text itself carried is dropped, so that the model
// cannot move a day by rewriting it.
func stamp(doc, today string, sources ...string) string {
	known := map[string]string{}
	for _, source := range sources {
		for body, day := range itemDates(source) {
			if old, seen := known[body]; !seen || day < old {
				known[body] = day
			}
		}
	}
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		mark, _, body, ok := splitItem(line)
		if !ok {
			continue
		}
		day := known[body]
		if day == "" {
			day = today
		}
		lines[i] = mark + "[" + day + "] " + body
	}
	return strings.Join(lines, "\n")
}

// backdate gives the items of a document from before the days were kept
// the day they last changed, as far as the nightly versions tell: the
// oldest version of the unbroken run, counted back from the newest, that
// carries the same text. An item no version knows gets today.
func (k *Keeper) backdate(doc, today string) string {
	if !undated(doc) {
		return doc
	}
	paths, _ := filepath.Glob(filepath.Join(k.Dir, "versions", "*.md"))
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	since := map[string]string{} // text -> oldest day of its run so far
	broken := map[string]bool{}  // texts whose run has ended
	wanted := map[string]bool{}
	for _, line := range strings.Split(doc, "\n") {
		if _, day, body, ok := splitItem(line); ok && day == "" {
			wanted[body] = true
		}
	}
	for _, p := range paths {
		day := strings.TrimSuffix(filepath.Base(p), ".md")
		if _, err := time.Parse(time.DateOnly, day); err != nil || day > today {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		has := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			if _, _, body, ok := splitItem(line); ok {
				has[body] = true
			}
		}
		for body := range wanted {
			if broken[body] {
				continue
			}
			if has[body] {
				since[body] = day
			} else {
				broken[body] = true
			}
		}
	}
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		mark, day, body, ok := splitItem(line)
		if !ok || day != "" {
			continue
		}
		if day = since[body]; day == "" {
			day = today
		}
		lines[i] = mark + "[" + day + "] " + body
	}
	return strings.Join(lines, "\n")
}

// Date gives the items of a document from before the days were kept their
// days now, without waiting for the next night. It reports whether anything
// changed. The text of the items stays as it is.
func (k *Keeper) Date() (bool, error) {
	k.run.Lock()
	defer k.run.Unlock()
	doc := strings.TrimSpace(k.Document())
	if doc == "" || !undated(doc) {
		return false, nil
	}
	next := k.backdate(doc, k.now().In(k.Zone()).Format(time.DateOnly))
	if err := writeFile(k.Path, []byte(next+"\n"), 0o644); err != nil {
		return false, err
	}
	night := Night{At: k.now(), Accepted: true, Summary: "項目に、中身が最後に変わった日を付けました。本文は変えていません。"}
	k.record(night)
	if k.Saved != nil {
		k.Saved(night)
	}
	return true, nil
}

// carryDates gives the undated items of doc the day the same text has in
// from. Items unknown there stay undated, for backdate to settle.
func carryDates(doc, from string) string {
	if !undated(doc) {
		return doc
	}
	known := itemDates(from)
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if mark, day, body, ok := splitItem(line); ok && day == "" && known[body] != "" {
			lines[i] = mark + "[" + known[body] + "] " + body
		}
	}
	return strings.Join(lines, "\n")
}
