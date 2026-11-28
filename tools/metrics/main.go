// Command metrics prints the size of the Go code that is not tests: its
// lines, the functions of 100 lines or more, and the files of 1,000 lines or
// more. Given -base, it also prints how they changed since that revision and
// warns about each function that newly reached 100 lines. It never fails
// because of the numbers; the warnings are for the author to explain.
//
//	go run ./tools/metrics [-base origin/main]
//
// In GitHub Actions the table also goes to the job summary, and the warnings
// are written as workflow annotations.
package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	longFunction = 100
	longFile     = 1000
)

type function struct {
	Key   string // package directory and name, stable across moves within a package
	File  string
	Line  int
	Lines int
}

type file struct {
	Path  string
	Lines int
}

type snapshot struct {
	Lines     int
	Functions int
	Long      []function
	Files     []file
}

func main() {
	base := flag.String("base", "", "比べる版（Git のコミットやブランチ）。省くと、いまの数だけを出します")
	flag.Parse()
	if err := run(*base, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "metrics: "+err.Error())
		os.Exit(1)
	}
}

func run(base string, out io.Writer) error {
	head, err := measure(os.DirFS("."))
	if err != nil {
		return err
	}
	var before *snapshot
	if base != "" {
		if before, err = measureRevision(base); err != nil {
			// A push that starts a branch has no usable base; the numbers
			// of the head are still worth showing.
			fmt.Fprintf(out, "比べる版 %s を読めなかったので、いまの数だけを出します（%v）。\n\n", base, err)
			before = nil
		}
	}
	report := format(head, before, base)
	fmt.Fprint(out, report)
	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		f, err := os.OpenFile(summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("検査の結果の要約 %s を開けませんでした: %w", summary, err)
		}
		defer f.Close()
		if _, err := io.WriteString(f, report); err != nil {
			return fmt.Errorf("検査の結果の要約 %s に書けませんでした: %w", summary, err)
		}
	}
	if before != nil && os.Getenv("GITHUB_ACTIONS") == "true" {
		for _, f := range newlyLong(head, before) {
			fmt.Fprintf(out, "::warning file=%s,line=%d::%s が %d 行になりました。分けない理由を、変更の説明に書いてください。\n", f.File, f.Line, f.Key, f.Lines)
		}
	}
	return nil
}

// measureRevision reads the tree of a revision through git archive, so that
// the working tree is left as it is.
func measureRevision(rev string) (*snapshot, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "archive", "--format=tar", rev)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git archive %s: %v: %s", rev, err, strings.TrimSpace(stderr.String()))
	}
	files := map[string][]byte{}
	r := tar.NewReader(&stdout)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || !counted(h.Name) {
			continue
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		files[h.Name] = data
	}
	return measureFiles(files)
}

func measure(fsys fs.FS) (*snapshot, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name != "." && skippedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !counted(name) {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		files[name] = data
		return err
	})
	if err != nil {
		return nil, err
	}
	return measureFiles(files)
}

// counted follows what the go command builds: tests, testdata and
// directories starting with a dot or an underscore are left out.
func counted(name string) bool {
	if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
		return false
	}
	for _, dir := range strings.Split(path.Dir(name), "/") {
		if dir != "." && skippedDir(dir) {
			return false
		}
	}
	return true
}

func skippedDir(name string) bool {
	return name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func measureFiles(files map[string][]byte) (*snapshot, error) {
	s := &snapshot{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]int{}
	for _, name := range names {
		data := files[name]
		lines := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		s.Lines += lines
		if lines >= longFile {
			s.Files = append(s.Files, file{name, lines})
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			s.Functions++
			start, end := fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line
			if n := end - start + 1; n >= longFunction {
				key := path.Dir(name) + " " + funcName(fn)
				// Several init functions may share a name.
				if seen[key]++; seen[key] > 1 {
					key = fmt.Sprintf("%s#%d", key, seen[key])
				}
				s.Long = append(s.Long, function{key, filepath.ToSlash(name), start, n})
			}
		}
	}
	sort.Slice(s.Long, func(i, j int) bool { return s.Long[i].Lines > s.Long[j].Lines })
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Lines > s.Files[j].Lines })
	return s, nil
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	if idx, ok := t.(*ast.IndexExpr); ok {
		t = idx.X
	}
	if idx, ok := t.(*ast.IndexListExpr); ok {
		t = idx.X
	}
	pointer := ""
	if star, ok := t.(*ast.StarExpr); ok {
		pointer, t = "*", star.X
	}
	recv := "?"
	if id, ok := t.(*ast.Ident); ok {
		recv = id.Name
	}
	if pointer != "" {
		return "(*" + recv + ")." + fn.Name.Name
	}
	return recv + "." + fn.Name.Name
}

func newlyLong(head, before *snapshot) []function {
	old := map[string]bool{}
	for _, f := range before.Long {
		old[f.Key] = true
	}
	var out []function
	for _, f := range head.Long {
		if !old[f.Key] {
			out = append(out, f)
		}
	}
	return out
}

func format(head, before *snapshot, base string) string {
	var b strings.Builder
	b.WriteString("## コードの数（テストを除く Go）\n\n")
	if before == nil {
		b.WriteString("| 見るもの | いま |\n| --- | ---: |\n")
		fmt.Fprintf(&b, "| 行数 | %d |\n", head.Lines)
		fmt.Fprintf(&b, "| 関数 | %d |\n", head.Functions)
		fmt.Fprintf(&b, "| %d行以上の関数 | %d |\n", longFunction, len(head.Long))
		fmt.Fprintf(&b, "| %d行以上のファイル | %d |\n", longFile, len(head.Files))
	} else {
		fmt.Fprintf(&b, "比べた版は `%s` です。\n\n", base)
		b.WriteString("| 見るもの | 前 | いま | 増減 |\n| --- | ---: | ---: | ---: |\n")
		row := func(name string, was, now int) {
			fmt.Fprintf(&b, "| %s | %d | %d | %+d |\n", name, was, now, now-was)
		}
		row("行数", before.Lines, head.Lines)
		row("関数", before.Functions, head.Functions)
		row(fmt.Sprintf("%d行以上の関数", longFunction), len(before.Long), len(head.Long))
		row(fmt.Sprintf("%d行以上のファイル", longFile), len(before.Files), len(head.Files))
		if added := newlyLong(head, before); len(added) > 0 {
			fmt.Fprintf(&b, "\n%d行以上になった関数が、新しく%d個あります。分けない理由を、変更の説明に書いてください。\n\n", longFunction, len(added))
			for _, f := range added {
				fmt.Fprintf(&b, "- `%s`（`%s:%d`、%d行）\n", f.Key, f.File, f.Line, f.Lines)
			}
		}
	}
	fmt.Fprintf(&b, "\n### %d行以上の関数\n\n", longFunction)
	if len(head.Long) == 0 {
		b.WriteString("ありません。\n")
	} else {
		was := map[string]int{}
		if before != nil {
			for _, f := range before.Long {
				was[f.Key] = f.Lines
			}
		}
		b.WriteString("| 関数 | 場所 | 行数 | 増減 |\n| --- | --- | ---: | ---: |\n")
		for _, f := range head.Long {
			change := ""
			switch old, ok := was[f.Key]; {
			case before == nil:
			case !ok:
				change = "新しく"
			default:
				change = fmt.Sprintf("%+d", f.Lines-old)
			}
			fmt.Fprintf(&b, "| `%s` | `%s:%d` | %d | %s |\n", f.Key, f.File, f.Line, f.Lines, change)
		}
	}
	fmt.Fprintf(&b, "\n### %d行以上のファイル\n\n", longFile)
	if len(head.Files) == 0 {
		b.WriteString("ありません。\n")
	} else {
		b.WriteString("| ファイル | 行数 |\n| --- | ---: |\n")
		for _, f := range head.Files {
			fmt.Fprintf(&b, "| `%s` | %d |\n", f.Path, f.Lines)
		}
	}
	b.WriteString("\n")
	return b.String()
}
