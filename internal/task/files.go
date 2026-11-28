package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A task keeps what the user should look at in two places. artifacts/ is
// committed; files/ is not, and holds large or generated things that can be
// made again. files/ is cleared some time after the task is closed, unless
// the task is pinned.
const (
	ArtifactsDir = "artifacts"
	FilesDir     = "files"
	pinFile      = ".keep"

	MaxFileSize     = 50 << 20  // one file
	MaxTaskFiles    = 500 << 20 // everything in one task's files/
	LargeArtifact   = 200 << 10 // an artifact above this belongs in files/
	FilesKeptFor    = 90 * 24 * time.Hour
	filesKeptForDay = 90
)

type StoredFile struct {
	Path string // relative to files/
	Size int64
}

func (s *Store) StoredFiles(t Task) []StoredFile {
	var out []StoredFile
	base := filepath.Join(s.root, t.Dir, FilesDir)
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			rel, _ := filepath.Rel(base, p)
			out = append(out, StoredFile{filepath.ToSlash(rel), info.Size()})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Pinned reports whether the task's files/ are kept after it is closed.
func (s *Store) Pinned(t Task) bool {
	_, err := os.Stat(filepath.Join(s.root, t.Dir, FilesDir, pinFile))
	return err == nil
}

// SetPinned keeps the task's files/ after it is closed, or stops keeping them.
func (s *Store) SetPinned(ref string, keep bool) error {
	t, err := s.Get(ref)
	if err != nil {
		return err
	}
	pin := filepath.Join(t.Dir, FilesDir, pinFile)
	if keep == s.Pinned(t) {
		return nil
	}
	if keep {
		if err := s.records.WriteFileNoSync(pin, nil, 0o644); err != nil {
			return err
		}
		return s.Log(t.ID, Event{Type: EventFile, Action: "kept", Text: "files/ を、タスクを閉じたあとも残すことにしました。"})
	}
	if err := s.records.Remove(pin); err != nil {
		return err
	}
	return s.Log(t.ID, Event{Type: EventFile, Action: "unkept", Text: fmt.Sprintf("files/ を残す指定を外しました。タスクを閉じてから%d日で消えます。", filesKeptForDay)})
}

// ExpiresOn is the day the task's files/ will be removed, or "" when they
// stay: the task is open, pinned, or has no files.
func (s *Store) ExpiresOn(t Task) string {
	done, err := time.ParseInLocation(time.DateOnly, t.Done, s.Location())
	if err != nil || !t.Status.Closed() || s.Pinned(t) || len(s.StoredFiles(t)) == 0 {
		return ""
	}
	return done.AddDate(0, 0, filesKeptForDay).Format(time.DateOnly)
}

// PutFile saves a file into the task's files/ or artifacts/ directory and
// notes it in the history with its size and hash, so that Git keeps a trace of
// files it does not hold.
func (s *Store) PutFile(ref, kind, name string, r io.Reader, actor string) (StoredFile, error) {
	t, err := s.Get(ref)
	if err != nil {
		return StoredFile{}, err
	}
	if kind != FilesDir && kind != ArtifactsDir {
		return StoredFile{}, fmt.Errorf("置き場所 %q は使えません。files か artifacts を指定してください", kind)
	}
	clean := path.Clean("/" + filepath.ToSlash(name))[1:]
	if clean == "" || clean == "." || strings.HasSuffix(name, "/") {
		return StoredFile{}, errors.New("ファイルの名前を指定してください")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return StoredFile{}, errors.New("名前に「..」は使えません")
		}
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.HasPrefix(part, ".") {
			return StoredFile{}, fmt.Errorf("%q のように「.」で始まる名前は置けません", part)
		}
	}
	var used int64
	for _, f := range s.StoredFiles(t) {
		if f.Path != clean {
			used += f.Size
		}
	}
	// The file is read whole before anything is written, so that one over
	// the limits leaves the old file in place.
	data, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1))
	size := int64(len(data))
	switch {
	case err != nil:
		return StoredFile{}, err
	case size > MaxFileSize:
		return StoredFile{}, fmt.Errorf("%s は %d MiB を超えています。大きなデータは、元の環境に置いたままリンクしてください", clean, MaxFileSize>>20)
	case kind == FilesDir && used+size > MaxTaskFiles:
		return StoredFile{}, fmt.Errorf("このタスクの files/ が %d MiB を超えます。要らないものを消してから置いてください", MaxTaskFiles>>20)
	}
	if err := s.records.WriteFileNoSync(filepath.Join(t.Dir, kind, filepath.FromSlash(clean)), data, 0o644); err != nil {
		return StoredFile{}, err
	}
	note := "このファイルは Git の管理外です。"
	if kind == ArtifactsDir {
		note = "このファイルは Git で管理します。"
	}
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])[:16]
	entry := Event{Type: EventFile, By: actor, Action: "put", Path: kind + "/" + clean, Bytes: ptr(size), SHA256: sum,
		Text: fmt.Sprintf("%s/%s を置きました（%s、sha256 %s）。%s", kind, clean, HumanSize(size), sum, note)}
	return StoredFile{clean, size}, s.Log(t.ID, entry)
}

func (s *Store) RemoveFile(ref, name, actor string) error {
	t, err := s.Get(ref)
	if err != nil {
		return err
	}
	clean := path.Clean("/" + filepath.ToSlash(name))[1:]
	if clean == "" || strings.HasPrefix(path.Base(clean), ".") {
		return errors.New("ファイルの名前を指定してください")
	}
	file := filepath.Join(t.Dir, FilesDir, filepath.FromSlash(clean))
	if _, err := os.Lstat(filepath.Join(s.root, file)); err != nil || s.records.Remove(file) != nil {
		return fmt.Errorf("files/%s は見つかりませんでした", clean)
	}
	return s.Log(t.ID, Event{Type: EventFile, By: actor, Action: "removed", Path: FilesDir + "/" + clean, Text: fmt.Sprintf("files/%s を消しました。", clean)})
}

// ExpireFiles removes files/ of tasks closed longer ago than FilesKeptFor,
// except pinned ones. It returns the tasks it cleaned.
func (s *Store) ExpireFiles(now time.Time) []Task {
	today := now.In(s.Location()).Format(time.DateOnly)
	var cleaned []Task
	for _, t := range s.List() {
		on := s.ExpiresOn(t)
		if on == "" || on > today {
			continue
		}
		files := s.StoredFiles(t)
		var total int64
		for _, f := range files {
			total += f.Size
		}
		if err := s.records.RemoveAll(filepath.Join(t.Dir, FilesDir)); err != nil {
			continue
		}
		s.Log(t.ID, Event{Type: EventFile, Action: "purged", Text: fmt.Sprintf("タスクを閉じてから%d日たったので、files/ の%d件（%s）を消しました。", filesKeptForDay, len(files), HumanSize(total))})
		cleaned = append(cleaned, t)
	}
	return cleaned
}

func HumanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%dバイト", n)
}

// FileWarnings points at artifacts that are large enough to belong in
// files/, where they stay out of Git.
func (s *Store) FileWarnings(t Task) []Warning {
	var out []Warning
	base := filepath.Join(s.root, t.Dir, ArtifactsDir)
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() > LargeArtifact {
			rel, _ := filepath.Rel(base, p)
			out = append(out, Warning{0, fmt.Sprintf("artifacts/%s は %d KiB を超えています（%dバイト）。大きなファイルと自動生成したファイルは、Git の管理外にある files/ に置いてください。", filepath.ToSlash(rel), LargeArtifact>>10, info.Size())})
		}
		return nil
	})
	return out
}
