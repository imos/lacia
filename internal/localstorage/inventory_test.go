package localstorage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestScanAllocationAndSymlinks(t *testing.T) {
	root := testRoot(t)
	now := time.Now().Truncate(time.Second)
	old := now.Add(-100 * 24 * time.Hour)
	local := filepath.Join(root, "local")
	nested := filepath.Join(local, "a")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(nested, "body")
	writeFile(t, first, "content")
	if err := os.Link(first, filepath.Join(local, "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(nested, "skip")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(root, now)
	if err != nil || !report.Complete || len(report.Entries) != 2 {
		t.Fatalf("Scan = %+v, %v", report, err)
	}
	var fileStat, dirStat, localStat unix.Stat_t
	for path, stat := range map[string]*unix.Stat_t{first: &fileStat, nested: &dirStat, local: &localStat} {
		if err := unix.Stat(path, stat); err != nil {
			t.Fatal(err)
		}
	}
	a, b := report.Entries[0], report.Entries[1]
	if a.Name != "a" || a.Bytes != (fileStat.Blocks+dirStat.Blocks)*512 || a.AgeDays >= 1 || len(a.Symlinks) != 1 || a.Symlinks[0] != "a/skip" {
		t.Fatalf("entry a = %+v", a)
	}
	if b.Name != "b" || b.Bytes != 0 || b.AgeDays != 100 || !b.ReviewDue {
		t.Fatalf("entry b = %+v", b)
	}
	if report.Bytes != a.Bytes+localStat.Blocks*512 || report.LimitGiB != 10 || report.ExceedsLimit {
		t.Fatalf("report = %+v", report)
	}
}

func TestScanEmptyLocalAndLimit(t *testing.T) {
	root := testRoot(t)
	configDir := filepath.Join(root, ".lacia")
	if err := os.Mkdir(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "local-storage.json")
	writeFile(t, config, `{"limit_gib":0.000001}`)
	report, err := Scan(root, time.Now())
	if err != nil || !report.Complete || report.Bytes != 0 || report.LimitGiB != 0.000001 || report.ExceedsLimit || len(report.Entries) != 0 {
		t.Fatalf("Scan = %+v, %v", report, err)
	}
	if err = os.Mkdir(filepath.Join(root, "local"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "local", "data"), "more than a block")
	report, err = Scan(root, time.Now())
	if err != nil || !report.Complete || !report.ExceedsLimit {
		t.Fatalf("Scan with data = %+v, %v", report, err)
	}
	for _, value := range []string{`{"limit_gib":false}`, `{"limit_gib":null}`, `{"limit_gib":0}`, `{"limit_gib":1e999}`, `null`} {
		writeFile(t, config, value)
		if _, err := Scan(root, time.Now()); err == nil {
			t.Errorf("Scan accepted %s", value)
		}
	}
}

func TestScanRejectsLinkedRootAndLocal(t *testing.T) {
	root := testRoot(t)
	if err := os.Mkdir(filepath.Join(root, "repo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "repo"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(filepath.Join(root, "link"), time.Now()); err == nil {
		t.Fatal("accepted a linked repository")
	}
	if err := os.Symlink(filepath.Join(root, "repo"), filepath.Join(root, "local")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(root, time.Now()); err == nil {
		t.Fatal("accepted a linked local directory")
	}
}

func testRoot(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
