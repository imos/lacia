package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTroubles(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "schedules"), 0o755)
	write := func(name, content string) {
		os.WriteFile(filepath.Join(root, "schedules", name), []byte(content), 0o644)
	}
	write("fine.md", "---\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("failing.md", "---\ntitle: 失敗が続く確認\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("asleep.md", "---\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("once.md", "---\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("broken.md", "---\nevery: 7m\n---\n\n割り切れない間隔。\n")
	write("off.md", "---\ndaily: \"09:00\"\nenabled: false\n---\n\n止めてあります。\n")
	log := &RunLog{Root: root, State: filepath.Join(t.TempDir(), "runs.json")}
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	record := func(name, outcome string, days ...int) {
		for _, d := range days {
			if err := log.write(name, Run{At: day(d), Finished: day(d), Outcome: outcome}, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	record("fine", RunFailed, 20)
	record("fine", RunNothing, 21)
	record("failing", RunDone, 20)
	record("failing", RunFailed, 21, 22)
	record("asleep", RunMissed, 21, 22, 23)
	record("once", RunFailed, 22)
	record("off", RunFailed, 21, 22)

	got := map[string]string{}
	for _, trouble := range Troubles(root, Load(root), map[string]DeviceStatus{"desk": {Status: WaitingAuth}, "other": {Status: "ready"}}) {
		got[trouble.Key] = trouble.Title + " " + trouble.Text
	}
	for key, want := range map[string]string{
		"schedule:failing:failed":    "失敗が続く確認 実行されなかった回を除くと、直近2件の記録はすべて失敗でした。",
		"schedule:asleep:missed":     "直近の記録では、3回続けて予約を実行していません。",
		"schedule:broken:definition": "定義に問題がある",
		"device:desk:auth":           "認証を待っています",
	} {
		if !strings.Contains(got[key], want) {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if len(got) != 4 {
		t.Errorf("troubles = %v", got)
	}
}
