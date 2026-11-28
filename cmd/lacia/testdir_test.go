package main

import (
	"path/filepath"
	"testing"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/schedule"
)

// testDir opens dir as a directory lacia may write in.
func testDir(t *testing.T, dir string) *disk.Dir {
	t.Helper()
	d, err := disk.OpenTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// stateFile opens the directory of path, and names the file in it.
func stateFile(t *testing.T, path string) schedule.StateFile {
	t.Helper()
	return schedule.StateFile{Dir: testDir(t, filepath.Dir(path)), Name: filepath.Base(path)}
}
