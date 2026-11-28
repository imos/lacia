// Package mdlink finds the relative links of Markdown files and works out
// where they point inside a repository. The checks of a record repository
// (lacia doctor) use it.
package mdlink

import (
	"path"
	"regexp"
	"strings"
)

// link matches [text](dest) and ![alt](dest), with an optional title. The
// destination is the first group.
var link = regexp.MustCompile(`!?\[[^\]\n]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// scheme is the start of an absolute address, such as https: or mailto:.
var scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// Link is the destination of one link in a line, with its byte offsets.
type Link struct {
	Start, End int
	Dest       string
}

// Find returns the links of one line.
func Find(line string) []Link {
	var out []Link
	for _, m := range link.FindAllStringSubmatchIndex(line, -1) {
		out = append(out, Link{Start: m[2], End: m[3], Dest: line[m[2]:m[3]]})
	}
	return out
}

// Fence reports whether a line opens or closes a fenced block of code,
// inside which nothing is a link.
func Fence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// Resolve works out the repository path a link of the file at from (a
// repository path) points at, and the "#..." part after it. It returns
// false for what is not a relative link inside the repository: an address
// with a scheme, a link that starts with #, / or ~, and one that leaves the
// repository.
func Resolve(from, dest string) (target, fragment string, ok bool) {
	if dest == "" || scheme.MatchString(dest) || strings.ContainsAny(dest[:1], "#/~") {
		return "", "", false
	}
	p, frag, found := strings.Cut(dest, "#")
	if found {
		fragment = "#" + frag
	}
	target = path.Join(path.Dir(from), p)
	if target == ".." || strings.HasPrefix(target, "../") {
		return "", "", false
	}
	return target, fragment, true
}
