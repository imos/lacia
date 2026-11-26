package calendar

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

func TestWeekDateColorsAndHolidayNames(t *testing.T) {
	for _, tc := range []struct{ stamp, class, holiday string }{
		{"2026-11-21T00:00:00+09:00", "is-saturday", ""},
		{"2026-11-22T00:00:00+09:00", "is-sunday", ""},
		{"2026-11-23T00:00:00+09:00", "is-holiday", "勤労感謝の日"},
		{"2026-05-06T00:00:00+09:00", "is-holiday", "休日"},
		{"2025-05-03T00:00:00+09:00", "is-holiday", "憲法記念日"},
		{"2026-11-20T14:59:59Z", "", ""},
		{"2026-11-20T15:00:00Z", "is-saturday", ""},
		{"2026-11-22T15:00:00Z", "is-holiday", "勤労感謝の日"},
		{"2040-01-02T00:00:00+09:00", "", ""},
	} {
		t.Run(tc.stamp, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.stamp)
			if err != nil {
				t.Fatal(err)
			}
			view := Build(Snapshot{}, now)
			day := view.Week.Days[0]
			if got := weekDateClass(now); got != tc.class {
				t.Errorf("class = %q, want %q", got, tc.class)
			}
			if day.DateClass != tc.class || day.HolidayName != tc.holiday || day.Date != now.In(JST).Format("2006-01-02") {
				t.Fatalf("week day = %+v, want class %q holiday %q JST date", day, tc.class, tc.holiday)
			}
		})
	}
}

func TestWeekHolidayMarkupIsAccessibleAndScoped(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-21T10:00:00+09:00")
	view := Build(Snapshot{}, now)
	tmpl, err := AddTemplate(template.New("calendar"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "calendar", view); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		`class="calendar-week-date is-saturday">11/21(土)</p>`,
		`class="calendar-week-date is-sunday">11/22(日)</p>`,
		`class="calendar-week-date is-holiday">11/23(月) <span role="img" aria-label="祝日：勤労感謝の日" title="勤労感謝の日">🎌</span></p>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	if got := strings.Count(html, "🎌"); got != 1 {
		t.Errorf("flags = %d, want 1 (Sunday is not a holiday)", got)
	}
	if got := strings.Count(html, `calendar-week-date is-`); got != 3 {
		t.Errorf("colored headers = %d, want 3", got)
	}
	for _, daily := range view.Days {
		if strings.Contains(daily.DateLabel, "🎌") {
			t.Error("holiday decoration reached daily dates")
		}
	}
	if !strings.Contains(CSS, ".calendar-week-date.is-saturday{color:#1760b8}") || !strings.Contains(CSS, ".calendar-week-date.is-holiday,.calendar-week-date.is-sunday{color:#c52b37}") {
		t.Error("week date color rules missing")
	}
}
