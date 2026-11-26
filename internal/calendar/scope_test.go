package calendar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calendars.json")
	os.WriteFile(path, []byte(`[{"account":"tanaka@example.com","calendar_id":"primary","label":"会社"}]`), 0o644)
	scope, err := LoadScope(path)
	if err != nil || len(scope) != 1 || scope[0].Label != "会社" || scope[0].CalendarID != "primary" {
		t.Fatalf("scope = %+v, err = %v", scope, err)
	}
	for _, bad := range []string{
		`[{"account":"a@example.com","calendar_id":"primary"}]`,
		`[{"account":"a@example.com","calendar_id":"primary","label":"x"},{"account":"a@example.com","calendar_id":"primary","label":"y"}]`,
		`{"account":"a@example.com"}`,
	} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, err := LoadScope(path); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
