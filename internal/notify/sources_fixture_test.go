package notify

// The tests, and the scenarios recorded from the Python original, use these
// made-up calendars. The binary carries none; the server says which
// calendars it shows.
func init() {
	meetingSources = [][2]string{
		{"owner@example.com", "primary"},
		{"family@example.com", "example-shared@group.calendar.google.com"},
	}
}
