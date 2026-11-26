package web

import (
	"bytes"
	"html/template"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

var routineFile = regexp.MustCompile(`^(?:areas/[^/]+/)?routines/(S[0-9]+)(?:/(.*))?$`)

var taskFile = regexp.MustCompile(`^(?:areas/)?[a-z]+/tasks/[0-9]{4}-[0-9]{2}/([A-Z][0-9]+)(?:-[a-z0-9-]+)?(?:/(.*))?$`)

// renderMarkdown turns Markdown that lives in repoDir into HTML. Raw HTML
// in the source is not passed through, and relative links are pointed at
// the pages that show those files.
func renderMarkdown(src, repoDir string) template.HTML {
	source := []byte(src)
	doc := markdown.Parser().Parse(text.NewReader(source))
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			n.Destination = []byte(pageURL(repoDir, string(n.Destination)))
		case *ast.Image:
			n.Destination = []byte(pageURL(repoDir, string(n.Destination)))
		}
		return ast.WalkContinue, nil
	})
	var out bytes.Buffer
	if err := markdown.Renderer().Render(&out, source, doc); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(out.String())
}

// pageURL maps a link written relative to repoDir onto this site.
func pageURL(repoDir, dest string) string {
	if dest == "" || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "/") {
		return dest
	}
	if u, err := url.Parse(dest); err != nil || u.Scheme != "" || u.Host != "" {
		return dest
	}
	file, fragment, _ := strings.Cut(dest, "#")
	if unescaped, err := url.PathUnescape(file); err == nil {
		file = unescaped
	}
	target := repoURL(path.Join(repoDir, file))
	if fragment != "" && !strings.HasPrefix(target, "/tasks/") {
		target += "#" + fragment
	}
	return target
}

// repoURL returns the page that shows a repository path.
func repoURL(repoPath string) string {
	if m := routineFile.FindStringSubmatch(repoPath); m != nil && (m[2] == "" || m[2] == "routine.md") {
		return "/routines/" + m[1]
	}
	if m := taskFile.FindStringSubmatch(repoPath); m != nil {
		if m[2] == "" || m[2] == "task.md" {
			return "/tasks/" + m[1]
		}
		return "/tasks/" + m[1] + "/files/" + escapePath(m[2])
	}
	return "/files/" + escapePath(repoPath)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// summary flattens the first paragraph of Markdown into one short line for
// lists.
func summary(md string, limit int) string {
	para, _, _ := strings.Cut(strings.TrimSpace(md), "\n\n")
	para = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(para, "$1")
	para = strings.Join(strings.Fields(strings.NewReplacer("**", "", "`", "").Replace(para)), " ")
	if r := []rune(para); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return para
}
