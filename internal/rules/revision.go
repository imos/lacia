package rules

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/chat"
)

// SessionRules is the rendered rules used at conversation startup and the
// fingerprints used to notice changes. The fingerprints have no live context
// such as the current date or memory files.
type SessionRules struct {
	Core     string   `json:"core"`
	Behavior string   `json:"behavior"`
	Revision Revision `json:"revision"`
}

// Revision maps rule document names to fingerprints.
type Revision map[string]string

// ReadSessionRules renders the same documents as rules, including repository
// replacements and additions.
func ReadSessionRules(root, owner string) (SessionRules, error) {
	s := SessionRules{Behavior: strings.TrimSpace(chat.Behavior), Revision: Revision{}}
	add := func(name, text string) {
		s.Revision[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	}
	add("案内の直接の宛先", s.Behavior)
	if !Managed(root) {
		if root != "" {
			if data, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err == nil {
				add("AGENTS.md", string(data))
			}
		}
		return s, nil
	}
	for _, doc := range List(root, owner) {
		text, err := Load(root, doc.Name, owner)
		if err != nil {
			return SessionRules{}, err
		}
		if doc.Name == Core {
			s.Core = text
		}
		add(doc.Name, text)
	}
	return s, nil
}

// AddBehavior includes the policy once, even if it is already in Core.
func (s SessionRules) AddBehavior(text string) string {
	if s.Behavior == "" || strings.Contains(text, s.Behavior) {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n" + s.Behavior + "\n"
}

// Notice names the documents that changed. An older conversation with no
// recorded revision receives a single generic reminder.
func (rev Revision) Notice(previous Revision) string {
	if len(rev) == 0 {
		return ""
	}
	const message = "決まりの版が変わりました。"
	read := "`lacia rules` で読み直してください。"
	if _, own := rev["AGENTS.md"]; own {
		read = "`AGENTS.md` と参照先を読み直してください。"
	}
	if len(previous) == 0 {
		return message + read
	}
	var changed []string
	for name, value := range rev {
		if previous[name] != value {
			changed = append(changed, name)
		}
	}
	for name := range previous {
		if _, exists := rev[name]; !exists {
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	sort.Strings(changed)
	return message + read + " 変わった文書は " + strings.Join(changed, "、") + " です。"
}
