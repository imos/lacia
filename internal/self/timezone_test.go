package self

import (
	"context"
	"testing"
	"time"
)

func TestNightTimerUsesConfiguredZoneAcrossDST(t *testing.T) {
	k := keeper(t)
	loc, _ := time.LoadLocation("America/New_York")
	k.Location = loc
	k.At = "01:30"
	runs := 0
	k.Think = func(context.Context, string) (string, error) { runs++; return answer(first, ""), nil }
	for _, utc := range []string{"2026-11-01T05:29:00Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"} {
		now, _ := time.Parse(time.RFC3339, utc)
		k.Now = func() time.Time { return now }
		k.AddNote(Note{Text: "気づいたことです。"})
		k.Tick(context.Background(), now)
	}
	if runs != 1 {
		t.Fatalf("night ran %d times", runs)
	}
}
