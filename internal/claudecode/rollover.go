package claudecode

import (
	"errors"
	"os"
	"path/filepath"
)

// Rollover archives the current session reference and records the night in
// one rename. The next Run starts a new session. Archived references remain
// in private state, separate from the active pointer and the conversation.
func (r *Runner) Rollover(night string) (bool, error) {
	if r.cfg.Ephemeral || !r.mu.TryLock() {
		return false, nil
	}
	defer r.mu.Unlock()
	path := r.sessionFile()
	archive := path + ".retired-" + night
	if _, err := os.Lstat(archive); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(path, archive); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A first session created later in this window already starts fresh.
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return false, err
			}
			marker, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(err, os.ErrExist) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return false, marker.Close()
		}
		return false, err
	}
	return true, nil
}
