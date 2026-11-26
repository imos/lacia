package self

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestRecentRecordsFindsOrdinaryChangesWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	for name, at := range map[string]time.Time{
		"areas/work/tasks/task.md":      now.Add(-time.Minute),
		"memory/profile.md":             now.Add(-2 * time.Minute),
		"memory/old.md":                 now.Add(-48 * time.Hour),
		"memory/future.md":              now.Add(time.Minute),
		"memory/self.md":                now,
		"areas/work/tasks/files/raw.md": now,
		"local/credentials.md":          now,
		"log/notices/daily.md":          now,
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "local", "credentials.md"), filepath.Join(root, "memory", "outside.md")); err != nil {
		t.Fatal(err)
	}
	got := RecentRecords(root, filepath.Join(root, "memory", "self.md"))(now.Add(-24*time.Hour), now)
	if want := []string{"areas/work/tasks/task.md", "memory/profile.md"}; !slices.Equal(got, want) {
		t.Fatalf("changed = %q, want %q", got, want)
	}
}
