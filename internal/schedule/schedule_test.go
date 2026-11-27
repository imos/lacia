package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, task.JST)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWhenPreservesSeconds(t *testing.T) {
	for _, c := range []struct{ interval, want string }{
		{"90s", "90秒ごと"},
		{"84.375s", "84.375秒ごと"},
	} {
		var j Job
		if err := parse("---\nevery: "+c.interval+"\n---\n\n確認します。\n", &j); err != nil {
			t.Fatal(err)
		}
		if got := j.When(); got != c.want {
			t.Errorf("When() for %s = %q, want %q", c.interval, got, c.want)
		}
	}
}

func TestTick(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "schedules"), 0o755)
	write := func(name, content string) {
		os.WriteFile(filepath.Join(root, "schedules", name), []byte(content), 0o644)
	}
	write("calendar.md", "---\ntitle: 予定の取得\nevery: 30m\nconversation: side\n---\n\n予定を取得してください。\n")
	write("github.md", "---\ndaily: \"09:00\"\n---\n\nGitHubを確認してください。\n")
	write("renewal.md", "---\nat: 2026-11-30 09:00\n---\n\n契約の更新を案内してください。\n")
	write("off.md", "---\nevery: 1h\nenabled: false\n---\n\n止めてある仕事。\n")
	write("broken.md", "---\nevery: 7m\n---\n\n割り切れない間隔。\n")
	write("README.md", "説明\n")

	jobs := Load(root)
	if len(jobs) != 5 {
		t.Fatalf("loaded %d jobs", len(jobs))
	}
	for _, j := range jobs {
		switch j.Name {
		case "broken", "renewal": // a job that runs once is a task, not a schedule
			if j.Err == "" || j.Enabled {
				t.Errorf("broken definition was accepted: %+v", j)
			}
		case "calendar":
			if !j.Side || j.When() != "30分ごと" || j.Title != "予定の取得" {
				t.Errorf("calendar = %+v", j)
			}
		}
	}

	var got []string
	late := map[string]string{}
	r := &Runner{Root: root, State: filepath.Join(t.TempDir(), "state", "schedules.json"), Submit: func(id string, job Job) error {
		got = append(got, id)
		late[id] = job.Late
		return nil
	}}
	step := func(now string) string {
		got = nil
		if err := r.Tick(at(now)); err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, " ")
	}
	if s := step("2026-11-12 08:59"); s != "sched-calendar-2026-11-12-0830" {
		t.Errorf("08:59: %s", s)
	}
	if s := step("2026-11-12 08:59"); s != "" {
		t.Errorf("the same slot ran twice: %s", s)
	}
	if s := step("2026-11-12 09:01"); s != "sched-calendar-2026-11-12-0900 sched-github-2026-11-12-0900" {
		t.Errorf("09:01: %s", s)
	}
	// The server was down overnight: only the current slot runs, once. The
	// daily job is five hours late, which is still within the grace period.
	if s := step("2026-11-13 14:10"); s != "sched-calendar-2026-11-13-1400 sched-github-2026-11-13-0900" {
		t.Errorf("after an outage: %s", s)
	}
	if late["sched-github-2026-11-13-0900"] != "" && !strings.Contains(late["sched-github-2026-11-13-0900"], "11月13日 09:00") {
		t.Errorf("late note = %q", late["sched-github-2026-11-13-0900"])
	}
	// The server slept through the time: the job runs when it is back,
	// however late, and is told so.
	if s := step("2026-11-14 16:00"); s != "sched-calendar-2026-11-14-1600 sched-github-2026-11-14-0900" {
		t.Errorf("a daily job slept through: %s", s)
	}
	if n := late["sched-github-2026-11-14-0900"]; !strings.Contains(n, "11月14日 09:00") || strings.Contains(n, "回分") {
		t.Errorf("late note = %q", n)
	}
	// Three days asleep, back before today's time: one run stands for all.
	if s := step("2026-11-17 07:00"); s != "sched-calendar-2026-11-17-0700 sched-github-2026-11-16-0900" {
		t.Errorf("after three days: %s", s)
	}
	if n := late["sched-github-2026-11-16-0900"]; !strings.Contains(n, "11月16日 09:00") || !strings.Contains(n, "11月17日 07:00") ||
		!strings.Contains(n, "1回分は今回にまとめ、個別にはやり直しません。") || !strings.Contains(n, "いまから行っても役に立たない回は見送り") {
		t.Errorf("late note = %q", n)
	}
	if n := late["sched-calendar-2026-11-17-0700"]; !strings.Contains(n, "回分") {
		t.Errorf("an interval job was not told of the gap: %q", n)
	}
	if s := step("2026-11-17 07:01"); s != "" {
		t.Errorf("ran again: %s", s)
	}
	// A job newly written with a time long past does not start at once.
	write("evening.md", "---\ndaily: \"20:00\"\n---\n\n夜の確認。\n")
	if s := step("2026-11-17 07:02"); s != "" {
		t.Errorf("a job written with a past time ran: %s", s)
	}
	os.Remove(filepath.Join(root, "schedules", "evening.md"))
	if s := step("2026-11-30 09:00"); s != "sched-calendar-2026-11-30-0900 sched-github-2026-11-30-0900" {
		t.Errorf("the definition with at ran: %s", s)
	}
	if s := step("2026-11-30 09:29"); s != "" {
		t.Errorf("ran again: %s", s)
	}

	// A new process remembers what already ran.
	again := &Runner{Root: root, State: r.State, Submit: r.Submit}
	got = nil
	again.Tick(at("2026-11-30 09:29"))
	if len(got) != 0 {
		t.Errorf("after restart: %v", got)
	}
	if _, ok := again.Last()[tickKey]; ok || again.Last()["github"] == "" {
		t.Errorf("Last = %v", again.Last())
	}

	again.Tick(at("2026-12-02 08:00"))

	// State written before the time of the last look was kept: as before,
	// a job more than six hours late is skipped.
	os.WriteFile(r.State, []byte(`{"github": "2026-12-01T09:00"}`), 0o600)
	old := &Runner{Root: root, State: r.State, Submit: r.Submit}
	got = nil
	old.Tick(at("2026-12-02 16:00"))
	if s := strings.Join(got, " "); strings.Contains(s, "github") {
		t.Errorf("without the time of the last look: %s", s)
	}
}

func TestSituation(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 15, 6, 0, 0, 0, task.JST)
	got := Situation(root, "memory/now.md", now)
	if !strings.Contains(got, "きょうは 2026-11-15（日）です。") || !strings.Contains(got, "ここに添えられる記録はありません") {
		t.Fatalf("missing file: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := "- 2026-11-14〜2026-11-16：出張で大阪にいます。"
	if err := os.WriteFile(filepath.Join(root, "memory", "now.md"), []byte("# いまの状況\n\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = Situation(root, "memory/now.md", now)
	if !strings.Contains(got, line) || strings.Contains(got, "ここに添えられる記録はありません") || !strings.Contains(got, "目的に合わせる") {
		t.Fatalf("with file: %q", got)
	}
	long := strings.Repeat("あ", situationLimit+10)
	os.WriteFile(filepath.Join(root, "memory", "now.md"), []byte(long), 0o644)
	if got = Situation(root, "memory/now.md", now); strings.Contains(got, long) || !strings.Contains(got, "ここで切りました") {
		t.Fatalf("long file was not clipped")
	}
}

func TestBundledRoutineKeepsPreviousOccurrenceName(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lacia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"format":2,"areas":["work","personal"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(area, id, extra string) {
		t.Helper()
		dir := filepath.Join(root, "areas", area, "routines", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		data := "---\nid: " + id + "\ntitle: 点検\ndaily: 10:00\n" + extra + "---\n\n点検します。\n"
		if err := os.WriteFile(filepath.Join(dir, "routine.md"), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("personal", "S0001", "enabled: false\n")
	write("work", "S0002", "legacy_name: S1016\n")
	resolved, err := ResolveWait(root, "S1016")
	if err != nil || resolved != "S0002" {
		t.Fatalf("ResolveWait old ID = %q, %v", resolved, err)
	}
	r := Runner{
		Root: root, State: filepath.Join(root, "state.json"),
		Submit: func(id string, job Job) error {
			if id != "sched-S1016-2026-11-28-1000" || job.ID != "S0002" {
				t.Errorf("submission = %s (%s)", id, job.ID)
			}
			return nil
		},
	}
	if err := r.Tick(at("2026-11-28 10:01")); err != nil {
		t.Fatal(err)
	}
	if r.Last()["S0002"] == "" || r.Last()["S1016"] != r.Last()["S0002"] {
		t.Fatalf("last slots = %v", r.Last())
	}

	write("personal", "S1016", "")
	for _, job := range Load(root) {
		if job.ID != "S0001" && (job.Enabled || job.Err == "") {
			t.Errorf("accepted colliding definition: %+v", job)
		}
	}
}
