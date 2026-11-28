package mdlink

import "testing"

func TestFindAndResolve(t *testing.T) {
	links := Find("見る: [規則](../../AGENTS.md#口調) と ![図](a/b.png \"題\") と [外](https://example.com/x) と [ア](#上)")
	var dests []string
	for _, l := range links {
		dests = append(dests, l.Dest)
	}
	if len(dests) != 4 || dests[0] != "../../AGENTS.md#口調" || dests[1] != "a/b.png" {
		t.Fatalf("dests = %q", dests)
	}
	for _, c := range []struct {
		from, dest, target, frag string
		ok                       bool
	}{
		{"work/tasks/x.md", "../../AGENTS.md#口調", "AGENTS.md", "#口調", true},
		{"work/tasks/x.md", "a/b.png", "work/tasks/a/b.png", "", true},
		{"AGENTS.md", "../outside.md", "", "", false},
		{"AGENTS.md", "https://example.com", "", "", false},
		{"AGENTS.md", "/abs", "", "", false},
		{"AGENTS.md", "~/x", "", "", false},
		{"AGENTS.md", "#only", "", "", false},
		{"README.md", "work/", "work", "", true},
	} {
		target, frag, ok := Resolve(c.from, c.dest)
		if target != c.target || frag != c.frag || ok != c.ok {
			t.Errorf("Resolve(%q, %q) = %q %q %v", c.from, c.dest, target, frag, ok)
		}
	}
}
