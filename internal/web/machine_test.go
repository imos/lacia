package web

import (
	"strings"
	"testing"
)

// What the server found on its machine is shown on the home page, with the
// commands that fix it, and nothing is shown when nothing was found.
func TestHomeShowsMachineIssues(t *testing.T) {
	for _, issues := range [][]MachineIssue{nil, {{Problem: "サーバーから codex が見つかりません。", Fix: "lacia setup", Guide: "https://example.com/guide#codex"}}} {
		store, _ := setup(t)
		h, err := (&Server{Store: store, Version: "test", Machine: func() []MachineIssue { return issues }}).Handler()
		if err != nil {
			t.Fatal(err)
		}
		_, page, _ := get(t, h, "/")
		shown := strings.Contains(page, "この端末で直すこと")
		if shown != (len(issues) > 0) || len(issues) > 0 && (!strings.Contains(page, "<pre>lacia setup</pre>") || !strings.Contains(page, `href="https://example.com/guide#codex"`)) {
			t.Errorf("issues %v: shown = %v\n%s", issues, shown, page)
		}
	}
}
