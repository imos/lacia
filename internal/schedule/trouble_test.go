package schedule

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A routine that keeps failing is reviewed through its receipts, so only
// the runs it missed, a broken definition and a device waiting to sign in
// are troubles.
func TestTroubles(t *testing.T) {
	root := newRecords(t)
	write := func(id, content string) {
		putDefinition(t, root, "areas/work/routines/"+id+"/routine.md", "---\nid: "+id+"\n"+strings.TrimPrefix(content, "---\n"))
	}
	write("S1001", "---\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("S1002", "---\ntitle: 失敗が続く確認\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("S1003", "---\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("S1004", "---\ndaily: \"09:00\"\n---\n\n確認します。\n")
	write("S1005", "---\nevery: 7m\n---\n\n割り切れない間隔。\n")
	write("S1006", "---\ndaily: \"09:00\"\nenabled: false\n---\n\n止めてあります。\n")
	log := &RunLog{Root: root, State: stateFile(t, filepath.Join(t.TempDir(), "runs.json"))}
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	record := func(name, outcome string, days ...int) {
		for _, d := range days {
			if err := log.write(name, Run{At: day(d), Finished: day(d), Outcome: outcome}, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	record("S1001", RunFailed, 20)
	record("S1001", RunNothing, 21)
	record("S1002", RunDone, 20)
	record("S1002", RunFailed, 21, 22)
	record("S1003", RunMissed, 21, 22, 23)
	record("S1004", RunFailed, 22)
	record("S1006", RunFailed, 21, 22)

	got := map[string]string{}
	for _, trouble := range Troubles(root, Load(root), map[string]DeviceStatus{"desk": {Status: WaitingAuth}, "other": {Status: "ready"}}) {
		got[trouble.Key] = trouble.Title + " " + trouble.Text
	}
	for key, want := range map[string]string{
		"schedule:S1003:missed":     "直近の記録では、3回続けて予約を実行していません。",
		"schedule:S1005:definition": "定義に問題がある",
		"device:desk:auth":          "認証を待っています",
	} {
		if !strings.Contains(got[key], want) {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("troubles = %v", got)
	}
}
