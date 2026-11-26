package documents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListMetadataAndNestedProcedures(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		path = filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(".lacia/config.json", `{"format":1,"areas":["work","personal"]}`)
	write("areas/work/procedures/tool/procedure.md", "---\ntitle: 手順\nwhen: 動かすとき\nupdated: 2026-11-24\nx_tool: yes\n---\n\n## 大まかな流れ\n\n動かします。\n")
	write("areas/personal/procedures/old.md", "# 古い手順\n")
	write("archive/procedures/ignored.md", "# 凍結済み\n")
	got, err := List(root, "procedures", "work")
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Title != "手順" || got[0].When != "動かすとき" || got[0].Updated != "2026-11-24" || got[0].Path != "areas/work/procedures/tool/procedure.md" {
		t.Fatal(got)
	}
	got, err = List(root, "procedures", "")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
}
