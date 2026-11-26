package gcal

import "github.com/imos/lacia/internal/calendar"

// The tests use the made-up calendars below. The binary carries
// none; a server is told its calendars with --calendars.
func init() {
	calendar.Scope = []calendar.ScopeEntry{
		{Account: "owner@example.com", CalendarID: "primary", Label: "会社", Color: "#1a73e8", RequireVerified: true},
		{Account: "family@example.com", CalendarID: "example-shared@group.calendar.google.com", Label: "Family", Color: "#0b8043"},
	}
}
