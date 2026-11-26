// Package localstorage inventories the working files under a record repository.
package localstorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const gib = 1 << 30

type Report struct {
	Root         string    `json:"root"`
	MeasuredAt   time.Time `json:"measured_at"`
	Bytes        int64     `json:"allocated_bytes"`
	LimitGiB     float64   `json:"limit_gib"`
	ExceedsLimit bool      `json:"exceeds_limit"`
	Complete     bool      `json:"complete"`
	Errors       []string  `json:"errors"`
	Entries      []Entry   `json:"entries"`
}

type Entry struct {
	Name            string   `json:"name"`
	Bytes           int64    `json:"allocated_bytes"`
	AgeDays         float64  `json:"age_days"`
	ReviewAfterDays int      `json:"review_after_days"`
	ReviewDue       bool     `json:"review_due"`
	Symlinks        []string `json:"symlinks"`
}

type inode struct{ device, number uint64 }

type inventory struct {
	root   string
	now    time.Time
	seen   map[inode]bool
	errors []string
}

// Scan reads only the selected repository. Directory descriptors keep the walk
// rooted even if a working directory is moved or replaced during the scan.
func Scan(path string, now time.Time) (Report, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return Report{}, err
	}
	root, err := openRoot(path)
	if err != nil {
		return Report{}, err
	}
	defer root.Close()

	limit, err := readLimit(root)
	if err != nil {
		return Report{}, err
	}
	report := Report{Root: path, MeasuredAt: now.UTC(), LimitGiB: limit, Complete: true, Errors: []string{}, Entries: []Entry{}}
	local, err := openDir(root, "local")
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return Report{}, err
	}
	defer local.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(int(local.Fd()), &stat); err != nil {
		return Report{}, err
	}
	walk := inventory{root: filepath.Join(path, "local"), now: now, seen: map[inode]bool{}, errors: []string{}}
	report.Bytes = walk.bytes(&stat)
	children, err := list(local)
	if err != nil {
		return Report{}, err
	}
	for _, child := range children {
		links := []string{}
		size, updated := walk.measure(local, child.Name(), child.Name(), &links)
		days := 90
		switch {
		case size >= gib:
			days = 7
		case size >= 100<<20:
			days = 30
		}
		age := now.Sub(updated).Hours() / 24
		report.Entries = append(report.Entries, Entry{
			Name: child.Name(), Bytes: size, AgeDays: max(0, age), ReviewAfterDays: days,
			ReviewDue: age >= float64(days), Symlinks: links,
		})
		report.Bytes += size
	}
	sort.SliceStable(report.Entries, func(i, j int) bool { return report.Entries[i].Bytes > report.Entries[j].Bytes })
	report.Errors = walk.errors
	report.Complete = len(report.Errors) == 0
	report.ExceedsLimit = float64(report.Bytes) > report.LimitGiB*gib
	return report, nil
}

// Open each component from its parent so symbolic links are never traversed.
func openRoot(path string) (*os.File, error) {
	dir, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	for name := range strings.SplitSeq(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if name == "" {
			continue
		}
		next, err := openDir(dir, name)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}

func openDir(parent *os.File, name string) (*os.File, error) {
	return openAt(parent, name, unix.O_DIRECTORY)
}

func openAt(parent *os.File, name string, flags int) (*os.File, error) {
	path := filepath.Join(parent.Name(), name)
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func readLimit(root *os.File) (float64, error) {
	const defaultLimit = 10
	dir, err := openDir(root, ".lacia")
	if errors.Is(err, os.ErrNotExist) {
		return defaultLimit, nil
	}
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	file, err := openAt(dir, "local-storage.json", unix.O_NONBLOCK)
	if errors.Is(err, os.ErrNotExist) {
		return defaultLimit, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New(".lacia/local-storage.json は通常のファイルにしてください")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return 0, err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0, fmt.Errorf(".lacia/local-storage.json を読めません: %w", err)
	}
	if cfg == nil {
		return 0, errors.New(".lacia/local-storage.json には JSON のオブジェクトを指定してください")
	}
	raw, ok := cfg["limit_gib"]
	if !ok {
		return defaultLimit, nil
	}
	var limit float64
	if err := json.Unmarshal(raw, &limit); err != nil || limit <= 0 || math.IsInf(limit, 0) {
		return 0, errors.New("limit_gib には 0 より大きい有限の数を指定してください")
	}
	return limit, nil
}

func list(dir *os.File) ([]os.DirEntry, error) {
	entries, err := dir.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

func (w *inventory) bytes(stat *unix.Stat_t) int64 {
	key := inode{device: uint64(stat.Dev), number: uint64(stat.Ino)}
	if w.seen[key] {
		return 0
	}
	w.seen[key] = true
	return stat.Blocks * 512
}

func (w *inventory) measure(parent *os.File, name, relative string, links *[]string) (int64, time.Time) {
	fail := func(err error) (int64, time.Time) {
		w.errors = append(w.errors, fmt.Sprintf("%s: %v", filepath.Join(w.root, relative), err))
		return 0, w.now
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(err)
	}
	updated := time.Unix(stat.Mtim.Unix())
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		*links = append(*links, relative)
		return 0, updated
	case unix.S_IFDIR:
		dir, err := openDir(parent, name)
		if err != nil {
			return fail(err)
		}
		defer dir.Close()
		var opened unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &opened); err != nil {
			return fail(err)
		}
		if opened.Dev != stat.Dev || opened.Ino != stat.Ino {
			return fail(errors.New("点検中にディレクトリーが入れ替わりました"))
		}
		children, err := list(dir)
		if err != nil {
			return fail(err)
		}
		size := w.bytes(&stat)
		for _, child := range children {
			amount, changed := w.measure(dir, child.Name(), filepath.Join(relative, child.Name()), links)
			size += amount
			if changed.After(updated) {
				updated = changed
			}
		}
		return size, updated
	default:
		return w.bytes(&stat), updated
	}
}
