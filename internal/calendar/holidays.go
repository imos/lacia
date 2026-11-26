package calendar

import (
	"embed"
	"encoding/csv"
	"fmt"
	"strings"
	"time"
)

// holidayFiles is the Cabinet Office's published national holiday data.
// See docs/design/calendar.md for provenance and refresh steps.
//
//go:embed holidays.csv
var holidayFiles embed.FS

var holidays = loadHolidays()

func loadHolidays() map[string]string {
	data, err := holidayFiles.ReadFile("holidays.csv")
	if err != nil {
		panic(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		panic(err)
	}
	out := make(map[string]string, len(rows)-1)
	for _, row := range rows[1:] {
		if _, err := parseDate(row[0]); err != nil || strings.TrimSpace(row[1]) == "" {
			panic("invalid embedded holiday")
		}
		if _, exists := out[row[0]]; exists {
			panic("duplicate embedded holiday")
		}
		out[row[0]] = row[1]
	}
	return out
}

// weekDateClass uses the same JST date as the week header. A holiday wins
// over Saturday; Sunday alone does not imply a national holiday.
func weekDateClass(day time.Time) string { return weekDateClassLocal(day.In(JST)) }

func weekDateClassLocal(day time.Time) string {
	return dateClass(day, holidays[ymd(day)])
}

func dateClass(day time.Time, holiday string) string {
	if holiday != "" {
		return "is-holiday"
	}
	switch day.Weekday() {
	case time.Saturday:
		return "is-saturday"
	case time.Sunday:
		return "is-sunday"
	}
	return ""
}

func HolidaySetting(value string) (bool, error) {
	switch value {
	case "jp":
		return false, nil
	case "off":
		return true, nil
	default:
		return false, fmt.Errorf("calendar-holidays は jp または off を指定してください")
	}
}

// HolidayDataNote reports a scheduled maintenance need from the actual
// coverage, so replacing the embedded table updates the warning.
func HolidayDataNote(now time.Time) string {
	year := lastHolidayYear()
	if year == 0 || now.Year() < year {
		return ""
	}
	return fmt.Sprintf("同梱の日本の祝日データは%d年分までです。次の年のデータは内閣府のCSVが出たあとに更新します。", year)
}

func lastHolidayYear() int {
	latest := ""
	for date := range holidays {
		if date > latest {
			latest = date
		}
	}
	last, err := time.Parse("2006-01-02", latest)
	if err != nil {
		return 0
	}
	return last.Year()
}
