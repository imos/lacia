// Package disk is the only package in lacia that writes files. lacia writes
// in the records and in the tools directory (~/.lacia), which holds the
// server's state, each opened as an os.Root so no name can lead out of it.
// A command opens each directory once and hands the Dir to the parts that
// write in it (docs/design/safety.md). Every place lacia writes outside
// these is in outside.go.
package disk

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Dir is a directory lacia may write in. Names are relative to it; a name
// that leads outside, by ".." or by a symbolic link, is refused.
type Dir struct {
	path    string
	root    *os.Root
	dirMode os.FileMode
}

// OpenRecords opens the records, which must exist already.
func OpenRecords(path string) (*Dir, error) { return open(path, 0o755) }

// OpenTools opens the tools directory, making it when it is missing. It
// holds the server's state and secrets, so only the owner may read it.
func OpenTools(path string) (*Dir, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return open(path, 0o700)
}

func open(path string, dirMode os.FileMode) (*Dir, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Dir{path: path, root: root, dirMode: dirMode}, nil
}

func (d *Dir) Path() string { return d.path }

// ReadFile reads a file inside the directory. A conversation may leave a link
// that leads outside, and reading through the root keeps it from being read.
func (d *Dir) ReadFile(name string) ([]byte, error) { return d.root.ReadFile(name) }

// Open opens a file inside the directory for reading. It does not wait on
// a pipe that a conversation may have left in place of the file.
func (d *Dir) Open(name string) (*os.File, error) {
	return d.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// Sub opens a directory inside as a Dir of its own, so that names in it
// cannot lead out of it either.
func (d *Dir) Sub(name string) (*Dir, error) {
	root, err := d.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &Dir{path: filepath.Join(d.path, name), root: root, dirMode: d.dirMode}, nil
}

// MakeSub opens a directory inside as Sub does, making it, with missing
// parents, in mode perm when it is not there.
func (d *Dir) MakeSub(name string, perm os.FileMode) (*Dir, error) {
	if err := d.root.MkdirAll(name, perm); err != nil {
		return nil, d.fail("make directory", name, err)
	}
	return d.Sub(name)
}

func (d *Dir) Close() error { return d.root.Close() }

// WriteFile replaces the file whole: readers see the old content or the new,
// never a part. The content is synced before the rename and the directory
// after it, so a crash cannot leave an empty file in place of the old one.
// Missing parent directories are made.
func (d *Dir) WriteFile(name string, data []byte, perm os.FileMode) error {
	return d.replace(name, bytes.NewReader(data), perm, true)
}

// WriteFileNoSync replaces the file whole as WriteFile does, without waiting
// for the disk: after a crash, the file may be empty.
func (d *Dir) WriteFileNoSync(name string, data []byte, perm os.FileMode) error {
	return d.replace(name, bytes.NewReader(data), perm, false)
}

// WriteFrom replaces the file whole with what r yields, as WriteFile does.
func (d *Dir) WriteFrom(name string, r io.Reader, perm os.FileMode) error {
	return d.replace(name, r, perm, true)
}

func (d *Dir) replace(name string, r io.Reader, perm os.FileMode, sync bool) error {
	tmp := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp-"+rand.Text())
	err := d.copyTo(tmp, r, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm, sync)
	if err == nil {
		err = d.root.Rename(tmp, name)
	}
	if err != nil {
		d.root.Remove(tmp)
	} else if sync {
		err = d.syncDir(filepath.Dir(name))
	}
	return d.fail("write", name, err)
}

func (d *Dir) syncDir(name string) error {
	f, err := d.root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// CreateNew writes a file that must not exist yet. When it does, the error
// satisfies errors.Is(err, fs.ErrExist).
func (d *Dir) CreateNew(name string, data []byte, perm os.FileMode) error {
	return d.fail("write", name, d.copyTo(name, bytes.NewReader(data), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm, false))
}

// CreateNewSync writes a new file as CreateNew does, and waits for the disk:
// the file, and each directory from it up to this one, so that a claim it
// records outlasts a crash.
func (d *Dir) CreateNewSync(name string, data []byte, perm os.FileMode) error {
	err := d.copyTo(name, bytes.NewReader(data), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm, true)
	for dir := filepath.Dir(name); err == nil; dir = filepath.Dir(dir) {
		err = d.syncDir(dir)
		if dir == "." {
			break
		}
	}
	return d.fail("write", name, err)
}

func (d *Dir) Append(name string, data []byte, perm os.FileMode) error {
	return d.fail("write", name, d.copyTo(name, bytes.NewReader(data), appendFlag, perm, false))
}

// AppendSync appends as Append does, and waits for the disk, for a record
// that must outlast a crash.
func (d *Dir) AppendSync(name string, data []byte, perm os.FileMode) error {
	return d.fail("write", name, d.copyTo(name, bytes.NewReader(data), appendFlag, perm, true))
}

const appendFlag = os.O_WRONLY | os.O_CREATE | os.O_APPEND

// OpenAppend opens the file for a stream of appends, such as a log that
// another program writes to.
func (d *Dir) OpenAppend(name string, perm os.FileMode) (*os.File, error) {
	f, err := d.open(name, appendFlag, perm)
	return f, d.fail("open", name, err)
}

// OpenLock opens the file to read and write in place, making it when
// missing: a lock, or a small file kept under one. A link in its place is
// refused, so the lock cannot be another file.
func (d *Dir) OpenLock(name string, perm os.FileMode) (*os.File, error) {
	f, err := d.open(name, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, perm)
	return f, d.fail("open", name, err)
}

func (d *Dir) open(name string, flag int, perm os.FileMode) (*os.File, error) {
	if err := d.root.MkdirAll(filepath.Dir(name), d.dirMode); err != nil {
		return nil, err
	}
	f, err := d.root.OpenFile(name, flag, perm)
	// On macOS, openat with O_CREATE fails with ENOENT when another writer
	// makes the same file at that moment. The file is there by then.
	if errors.Is(err, fs.ErrNotExist) && flag&os.O_EXCL == 0 {
		f, err = d.root.OpenFile(name, flag, perm)
	}
	return f, err
}

func (d *Dir) copyTo(name string, r io.Reader, flag int, perm os.FileMode, sync bool) error {
	f, err := d.open(name, flag, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil && sync {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (d *Dir) MkdirAll(name string) error {
	return d.fail("make directory", name, d.root.MkdirAll(name, d.dirMode))
}

func (d *Dir) Rename(oldName, newName string) error {
	err := d.root.MkdirAll(filepath.Dir(newName), d.dirMode)
	if err == nil {
		err = d.root.Rename(oldName, newName)
	}
	return d.fail("rename "+oldName+" to", newName, err)
}

// MkdirTemp makes a new directory inside dir, making dir when missing, and
// returns its name. Only the owner may read either.
func (d *Dir) MkdirTemp(dir, prefix string) (string, error) {
	name := filepath.Join(dir, prefix+rand.Text())
	err := d.root.MkdirAll(dir, 0o700)
	if err == nil {
		err = d.root.Mkdir(name, 0o700)
	}
	return name, d.fail("make directory", name, err)
}

func (d *Dir) Lstat(name string) (fs.FileInfo, error) { return d.root.Lstat(name) }

// Remove and RemoveAll do nothing when the name is already gone.
func (d *Dir) Remove(name string) error {
	if err := d.RemoveFile(name); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveFile removes the name as Remove does, but reports one that is
// already gone, with an error satisfying errors.Is(err, fs.ErrNotExist),
// for callers that must know whether they removed it.
func (d *Dir) RemoveFile(name string) error { return d.fail("remove", name, d.root.Remove(name)) }

func (d *Dir) RemoveAll(name string) error {
	return d.fail("remove", name, d.root.RemoveAll(name))
}

// fail names the whole path, since the root's own errors name only the part
// inside it. The name is not cleaned, so a ".." that was refused shows.
func (d *Dir) fail(action, name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s%c%s: %w", action, d.path, filepath.Separator, name, err)
}

// RemoveTools removes the tools directory whole, for lacia uninstall. A
// link inside it, such as src pointing at a clone kept elsewhere, is
// removed and not followed.
func RemoveTools(path string) error {
	if filepath.Base(path) != ".lacia" && os.Getenv("LACIA_HOME") != path {
		return fmt.Errorf("%s は道具の置き場所ではないので、消しません", path)
	}
	return os.RemoveAll(path)
}
