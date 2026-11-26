// Package task reads and writes tasks in a record repository.
//
// A task is a directory holding task.md (front matter plus a short body),
// and optionally notes.md, log.md and artifacts/. See docs/design/records.md.
package task

import (
	"fmt"
	"strings"
)

// Field is one "key: value" line of the front matter. Order is preserved so
// that a rewrite by the server changes only the lines it means to change.
type Field struct {
	Key   string
	Value string
}

// Doc is a parsed task.md: front matter fields followed by a Markdown body.
type Doc struct {
	Fields []Field
	Body   string
}

// ParseDoc parses "---\nkey: value\n---\nbody". The front matter is a flat
// list of scalars; nested YAML is rejected so that machines and Codex agree
// on one simple shape.
func ParseDoc(src string) (Doc, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	rest, ok := strings.CutPrefix(src, "---\n")
	if !ok {
		return Doc{}, fmt.Errorf("1行目に --- がありません")
	}
	var d Doc
	for {
		line, after, found := strings.Cut(rest, "\n")
		if !found && line == "" {
			return Doc{}, fmt.Errorf("冒頭の項目が --- で閉じられていません")
		}
		rest = after
		if line == "---" {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || !validKey(key) {
			return Doc{}, fmt.Errorf("冒頭の行が「項目: 値」の形になっていません: %q", line)
		}
		value, err := unquote(strings.TrimSpace(value))
		if err != nil {
			return Doc{}, fmt.Errorf("冒頭の項目 %s: %w", key, err)
		}
		if d.Has(key) {
			return Doc{}, fmt.Errorf("冒頭に項目 %s が2回あります", key)
		}
		d.Fields = append(d.Fields, Field{key, value})
	}
	d.Body = strings.TrimLeft(rest, "\n")
	return d, nil
}

// String serializes the document. Empty values are dropped: an absent
// optional field and an empty one mean the same thing.
func (d Doc) String() string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, f := range d.Fields {
		if f.Value == "" {
			continue
		}
		b.WriteString(f.Key + ": " + quote(f.Value) + "\n")
	}
	b.WriteString("---\n")
	if body := strings.TrimRight(d.Body, "\n"); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	return b.String()
}

func (d Doc) Has(key string) bool {
	for _, f := range d.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func (d Doc) Get(key string) string {
	for _, f := range d.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// Set replaces the value in place, or appends the field when it is new.
func (d *Doc) Set(key, value string) {
	for i := range d.Fields {
		if d.Fields[i].Key == key {
			d.Fields[i].Value = value
			return
		}
	}
	d.Fields = append(d.Fields, Field{key, value})
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

// quote keeps the output valid YAML for the values we actually store, so
// GitHub and other tools can still read the front matter.
func quote(v string) string {
	plain := !strings.ContainsAny(v[:1], "\"'[]{}#&*!|>%@`-?:, ") &&
		!strings.Contains(v, ": ") && !strings.Contains(v, " #") &&
		!strings.HasSuffix(v, ":") && !strings.HasSuffix(v, " ") &&
		!strings.ContainsAny(v, "\n\t")
	if plain {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

func unquote(v string) (string, error) {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		inner := v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if c != '\\' {
				b.WriteByte(c)
				continue
			}
			i++
			if i == len(inner) {
				return "", fmt.Errorf("%s で、末尾のバックスラッシュに続く文字がありません", v)
			}
			switch inner[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(inner[i])
			}
		}
		return b.String(), nil
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'"), nil
	case strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{") || v == "|" || v == ">":
		return "", fmt.Errorf("値には1行の文字列を指定してください: %s", v)
	}
	return v, nil
}

// Section returns the text under a "## heading" in a Markdown body, up to
// the next heading of the same or a higher level.
func Section(body, heading string) string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			if in {
				break
			}
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == heading
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
