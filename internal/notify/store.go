package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/imos/lacia/internal/effect/disk"
)

var (
	ErrStateVersion     = errors.New("state_version_unsupported")
	ErrDirectoryPrivate = errors.New("state_directory_not_private")
)

// LoadState reads the state file. A missing file is a first run.
func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return nil, err
	}
	state := &State{}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if state.Version != Version {
		return nil, ErrStateVersion
	}
	normalize(state)
	return state, nil
}

// SaveState writes the state file atomically with mode 0600.
func SaveState(path string, state *State) error {
	normalize(state)
	return writeJSON(path, state)
}

func normalize(state *State) {
	if state.Outbox == nil {
		state.Outbox = []Notice{}
	}
	if state.Meetings == nil {
		state.Meetings = []Notice{}
	}
}

// WriteManifest writes manifest.json atomically with mode 0600.
func WriteManifest(path string, manifest Manifest) error {
	if manifest.Notices == nil {
		// The Swift decoder rejects null where it expects an array.
		manifest.Notices = []Notice{}
	}
	return writeJSON(path, manifest)
}

// ReadManifest reads manifest.json back.
func ReadManifest(path string) (Manifest, error) {
	var manifest Manifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("%s: %w", path, err)
	}
	return manifest, nil
}

// privateDirectory creates dir with mode 0700 and refuses one that is a
// symlink, belongs to someone else, or is open to group or others.
func privateDirectory(dir string) error {
	if err := disk.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	link, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if link.Mode()&fs.ModeSymlink != 0 || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return ErrDirectoryPrivate
	}
	return nil
}

func writeJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	data, err := marshal(value)
	if err != nil {
		return err
	}
	return disk.WriteFile(path, data, 0o600)
}
