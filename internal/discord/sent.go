package discord

import (
	"strconv"
	"time"
)

// SentAt is when Discord created the message with this ID; the time is part
// of the ID. It is the zero time when id is not a Discord ID.
func SentAt(id string) time.Time {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n>>22 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(n>>22) + 1420070400000)
}
