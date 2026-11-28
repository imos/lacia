// Package effect holds the packages through which lacia acts outside its own
// process, and this test, which finds such acts anywhere else.
package effect

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type kind string

const (
	command kind = "command"
	write   kind = "write"
	network kind = "network"
)

// home is the one package where each kind of act belongs.
var home = map[kind]string{
	command: "internal/effect/program",
	write:   "internal/effect/disk",
	network: "internal/effect/network",
}

// acts names, per import path, the functions and variables whose use acts
// outside the process. Every name in os/exec counts, so that only the
// program package imports it. Making an HTTP request does not count: only a
// client or a transport sends it, and those are made in the network package.
var acts = map[string]map[string]kind{
	"os": {
		"WriteFile": write, "Create": write, "CreateTemp": write, "MkdirTemp": write,
		"Rename": write, "Remove": write, "RemoveAll": write, "Mkdir": write, "MkdirAll": write,
		"Symlink": write, "Link": write, "Chmod": write, "Chown": write, "Lchown": write,
		"Chtimes": write, "Truncate": write, "OpenRoot": write, "OpenInRoot": write,
		"StartProcess": command,
	},
	"io/ioutil":             {"WriteFile": write, "TempFile": write, "TempDir": write},
	"syscall":               {"Exec": command, "ForkExec": command, "StartProcess": command},
	"golang.org/x/sys/unix": {"Exec": command},
	"net/http": {
		"Get": network, "Head": network, "Post": network, "PostForm": network,
		"DefaultClient": network, "DefaultTransport": network,
	},
	"net":                        {"Dial": network, "DialTimeout": network, "DialTCP": network, "DialUDP": network, "DialUnix": network, "DialIP": network},
	"github.com/coder/websocket": {"Dial": network},
}

// madeTypes count only where a value is made, not where the type is named.
var madeTypes = map[string]map[string]kind{
	"net/http": {"Client": network, "Transport": network},
	"net":      {"Dialer": network},
}

// readFlags are the os.OpenFile flags that do not write.
var readFlags = []string{"O_RDONLY", "O_NOFOLLOW", "O_CLOEXEC", "O_NONBLOCK"}

func TestBoundary(t *testing.T) {
	found := scanModule(t, "../..")
	for _, k := range []kind{command, write, network} {
		compare(t, k, found[k], exceptions[k])
	}
}

// compare fails both when a file acts more than its exception allows and
// when it acts less: a list that is not lowered would hide the next one.
func compare(t *testing.T, k kind, found, allowed map[string]int) {
	t.Helper()
	files := map[string]bool{}
	for file := range found {
		files[file] = true
	}
	for file := range allowed {
		files[file] = true
	}
	wrong := false
	for _, file := range slices.Sorted(maps.Keys(files)) {
		switch n, max := found[file], allowed[file]; {
		case n > max:
			t.Errorf("%s: %s が %d か所あります。%s のパッケージを使ってください", file, k, n-max, home[k])
			wrong = true
		case n < max:
			t.Errorf("%s: %s が %d か所に減りました。exceptions_test.go の数を %d にしてください", file, k, n, n)
			wrong = true
		}
	}
	if wrong {
		t.Logf("いまの %s の数です。\n%s", k, literal(found))
	}
}

func literal(counts map[string]int) string {
	var b strings.Builder
	for _, file := range slices.Sorted(maps.Keys(counts)) {
		fmt.Fprintf(&b, "\t\t%q: %d,\n", file, counts[file])
	}
	return b.String()
}

// scanModule counts the acts in each non-test file of the module, outside
// the act's home package. tools/ holds programs for developers, which are
// not built into lacia.
func scanModule(t *testing.T, root string) map[kind]map[string]int {
	t.Helper()
	found := map[kind]map[string]int{command: {}, write: {}, network: {}}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel == "tools" || entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for k, n := range scanFile(file) {
			if filepath.ToSlash(filepath.Dir(rel)) != home[k] {
				found[k][rel] += n
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func scanFile(file *ast.File) map[kind]int {
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	// pkg returns the import path and the name that expr refers to.
	pkg := func(expr ast.Expr) (string, string) {
		if sel, ok := expr.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				return imports[id.Name], sel.Sel.Name
			}
		}
		return "", ""
	}
	counts := map[kind]int{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			if path, name := pkg(node.Fun); path == "os" && name == "OpenFile" && len(node.Args) > 1 && writes(node.Args[1]) {
				counts[write]++
			}
		case *ast.CompositeLit:
			if path, name := pkg(node.Type); madeTypes[path][name] != "" {
				counts[madeTypes[path][name]]++
			}
		case *ast.SelectorExpr:
			path, name := pkg(node)
			if path == "os/exec" {
				counts[command]++
			} else if k := acts[path][name]; k != "" {
				counts[k]++
			}
		}
		return true
	})
	return counts
}

// writes reports whether an os.OpenFile flag may write: it does unless every
// part of it is a flag known not to.
func writes(flag ast.Expr) bool {
	switch flag := flag.(type) {
	case *ast.BinaryExpr:
		return writes(flag.X) || writes(flag.Y)
	case *ast.ParenExpr:
		return writes(flag.X)
	case *ast.SelectorExpr:
		return !slices.Contains(readFlags, flag.Sel.Name)
	}
	return true
}

// TestScanFile keeps the boundary test from passing by finding nothing.
func TestScanFile(t *testing.T) {
	const source = `package sample

import (
	"net/http"
	"os"
	run "os/exec"
	"syscall"
)

type holder struct{ client *http.Client }

func sample() {
	os.WriteFile("a", nil, 0o644)
	os.OpenFile("a", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	os.OpenFile("a", os.O_WRONLY|os.O_APPEND, 0)
	remove := os.Remove
	run.Command("git").Run()
	syscall.Exec("/bin/sh", nil, nil)
	_ = &http.Client{}
	http.Get("https://example.com/")
	os.ReadFile("a")
	_ = remove
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := scanFile(file)
	want := map[kind]int{write: 3, command: 2, network: 2}
	if !maps.Equal(got, want) {
		t.Errorf("scanFile = %v, want %v", got, want)
	}
}
