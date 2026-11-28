package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

func body(lines int) string {
	return strings.Repeat("\t_ = 0\n", lines)
}

func TestMeasureCountsLongFunctionsAndFiles(t *testing.T) {
	// 98 body lines with the func line and the closing brace make 100.
	long := "package a\n\nfunc Long() {\n" + body(98) + "}\n\ntype T struct{}\n\nfunc (*T) Short() {\n" + body(97) + "}\n"
	files := fstest.MapFS{
		"a/a.go":            {Data: []byte(long)},
		"a/a_test.go":       {Data: []byte("package a\n\nfunc TestX() {\n" + body(200) + "}\n")},
		"a/testdata/x.go":   {Data: []byte("package x\n")},
		"b/big.go":          {Data: []byte("package b\n" + strings.Repeat("// line\n", 999))},
		".hidden/hidden.go": {Data: []byte("package hidden\n")},
	}
	s, err := measure(files)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := strings.Count(long, "\n") + 1000
	if s.Lines != wantLines || s.Functions != 2 {
		t.Errorf("lines, functions = %d, %d; want %d, 2", s.Lines, s.Functions, wantLines)
	}
	if len(s.Long) != 1 || s.Long[0].Key != "a Long" || s.Long[0].Lines != 100 || s.Long[0].Line != 3 {
		t.Errorf("long functions = %+v", s.Long)
	}
	if len(s.Files) != 1 || s.Files[0].Path != "b/big.go" {
		t.Errorf("long files = %+v", s.Files)
	}
}

func TestNewlyLongComparesByPackageAndName(t *testing.T) {
	before := &snapshot{Long: []function{{Key: "a Long", Lines: 120}}}
	head := &snapshot{Long: []function{{Key: "a Long", Lines: 130}, {Key: "a (*T).Grown", Lines: 100}}}
	got := newlyLong(head, before)
	if len(got) != 1 || got[0].Key != "a (*T).Grown" {
		t.Errorf("newly long = %+v", got)
	}
	report := format(head, before, "main")
	if !strings.Contains(report, "新しく1個") || !strings.Contains(report, "| 100行以上の関数 | 1 | 2 | +1 |") {
		t.Errorf("report:\n%s", report)
	}
}
