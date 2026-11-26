package notify

import "time"

// MeetingRechecks returns the checkpoints reached since the last poll. The
// checkpoint belongs to the occurrence ID, so a moved event is checked anew.
func MeetingRechecks(state *State, now time.Time) map[string]int {
	out := map[string]int{}
	for _, n := range state.Meetings {
		if n.Expires <= seconds(now) {
			continue
		}
		stage := 0
		if seconds(now) >= n.Expires-600 {
			stage = 10
		}
		if seconds(now) >= n.Expires-60 {
			stage = 1
		}
		previous := state.MeetingChecks[n.ID]
		if stage != 0 && previous != 1 && (stage == 1 || previous != 10) {
			out[n.ID] = stage
		}
	}
	return out
}

// RecordMeetingRechecks records attempts, including failures: an unknown
// result keeps the planned notice instead of withholding delivery.
func RecordMeetingRechecks(state *State, checks map[string]int) {
	live := map[string]bool{}
	for _, n := range state.Meetings {
		live[n.ID] = true
	}
	if state.MeetingChecks == nil {
		state.MeetingChecks = map[string]int{}
	}
	for id := range state.MeetingChecks {
		if !live[id] {
			delete(state.MeetingChecks, id)
		}
	}
	for id, stage := range checks {
		state.MeetingChecks[id] = stage
	}
}

// NextMeetingCheck avoids sleeping past the next exact checkpoint.
func NextMeetingCheck(state *State, now time.Time) time.Duration {
	delay := 30 * time.Second
	for _, n := range state.Meetings {
		for _, lead := range []float64{600, 60} {
			d := time.Duration((n.Expires - lead - seconds(now)) * float64(time.Second))
			if d > 0 && d < delay {
				delay = d
			}
		}
	}
	return delay
}
