package project

import (
	"strings"
	"testing"
)

// Format 1 reads the order from 取りかかる順 only, and format 0 from 順序.
func TestOrderHeadingOfFormat1(t *testing.T) {
	src := "---\ntitle: 試し\n---\n\n## めざすこと\n\n試します。\n\n## 取りかかる順\n\n1. T1002 — 先に\n2. T1001\n\n## 順序\n\n1. T1001\n"
	p, err := ParseFormat(src, 1)
	if err != nil || len(p.Order) != 2 || p.Order[0].ID != "T1002" || p.Order[0].Reason != "先に" {
		t.Fatalf("format 1 = %+v, %v", p.Order, err)
	}
	if p, _ := Parse(src); len(p.Order) != 1 || p.Order[0].ID != "T1001" {
		t.Errorf("format 0 = %+v", p.Order)
	}
	out, err := MoveFormat(src, []string{"T1001", "T1002"}, "T1001", -1, 1)
	if err != nil || !strings.Contains(out, "## 取りかかる順\n\n1. T1001\n2. T1002 — 先に\n") || !strings.Contains(out, "## 順序\n\n1. T1001\n") {
		t.Errorf("move =\n%s\n%v", out, err)
	}
	out, _ = MoveFormat("---\ntitle: 空\n---\n\n## めざすこと\n\nなし。\n", []string{"T1001", "T1002"}, "T1002", -1, 1)
	if !strings.HasSuffix(out, "## 取りかかる順\n\n1. T1002\n2. T1001\n") {
		t.Errorf("new section =\n%s", out)
	}
}
