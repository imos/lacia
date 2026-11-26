package calendar

import (
	"sync"
	"time"
)

const rereadInterval = 30 * time.Second

// Reader loads the snapshot file at most every 30 seconds and keeps the last
// valid snapshot, so a broken or missing file never blanks the page.
type Reader struct {
	Location     *time.Location
	HideHolidays bool
	path         string

	mu      sync.Mutex
	last    Snapshot
	checked time.Time
	read    bool
	err     error
}

func NewReader(path string) *Reader {
	return &Reader{path: path}
}

// View builds the view at now. A non-nil error reports that the latest read
// failed; the view is still usable and says so through ReadError.
func (r *Reader) View(now time.Time) (View, error) {
	snap, loc, err := r.snapshot(now)
	return buildView(snap, now, err != nil, loc, r.HideHolidays), err
}

func (r *Reader) snapshot(now time.Time) (Snapshot, *time.Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if since := now.Sub(r.checked); !r.read || since >= rereadInterval || since < 0 {
		snap, err := Load(r.path)
		if err == nil {
			r.last = snap
		}
		r.err, r.checked, r.read = err, now, true
	}
	loc := r.Location
	if loc == nil {
		loc = JST
	}
	return r.last, loc, r.err
}
