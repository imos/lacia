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
	for _, c := range []struct{ from, target, want string }{
		{"areas/work/tasks/2026-11/T1/task.md", "AGENTS.md", "../../../../../AGENTS.md"},
		{"AGENTS.md", "areas/work/x.md", "areas/work/x.md"},
		{"areas/work/a/b.md", "areas/work/a/c.md", "c.md"},
		{"areas/work/a/b.md", "areas/work/a", "../a"},
		{"rules/local.md", "archive/journal/work/x.md", "../archive/journal/work/x.md"},
	} {
		if got := Relative(c.from, c.target); got != c.want {
			t.Errorf("Relative(%q, %q) = %q, want %q", c.from, c.target, got, c.want)
		}
	}
}
