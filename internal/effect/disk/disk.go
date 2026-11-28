// Package disk is the only package in lacia that writes files. lacia writes
// in two directories, the records and the tools directory (~/.lacia), and
// each is held as an os.Root so no name can lead out of it. Outside them,
// lacia writes only the two files in install.go.
package disk

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// MkdirAll makes the directory at path with missing parents, as
// os.MkdirAll does.
func MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func open(path string, dirMode os.FileMode) (*Dir, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Dir{path: path, root: root, dirMode: dirMode}, nil
}

func (d *Dir) Path() string { return d.path }

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

// WriteFile, WriteFileNoSync and Append are for code that keeps the path of
// a file rather than a Dir. A missing directory is made searchable by
// whoever may read the file: 0o755 for a file of 0o644, 0o700 for 0o600.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return at(path, perm, func(d *Dir, name string) error { return d.WriteFile(name, data, perm) })
}

func WriteFileNoSync(path string, data []byte, perm os.FileMode) error {
	return at(path, perm, func(d *Dir, name string) error { return d.WriteFileNoSync(name, data, perm) })
}

func Append(path string, data []byte, perm os.FileMode) error {
	return at(path, perm, func(d *Dir, name string) error { return d.Append(name, data, perm) })
}

// Remove removes the file at path, and does nothing when it is gone.
func Remove(path string) error {
	d, err := open(filepath.Dir(path), 0o700)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Remove(filepath.Base(path))
}

func at(path string, perm os.FileMode, write func(*Dir, string) error) error {
	dirMode := perm | perm&0o444>>2
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	d, err := open(filepath.Dir(path), dirMode)
	if err != nil {
		return err
	}
	defer d.Close()
	return write(d, filepath.Base(path))
}

// CreateNew writes a file that must not exist yet. When it does, the error
// satisfies errors.Is(err, fs.ErrExist).
func (d *Dir) CreateNew(name string, data []byte, perm os.FileMode) error {
	return d.fail("write", name, d.copyTo(name, bytes.NewReader(data), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm, false))
}

// Append adds data at the end of the file, making it when it is missing.
func (d *Dir) Append(name string, data []byte, perm os.FileMode) error {
	return d.fail("write", name, d.copyTo(name, bytes.NewReader(data), appendFlag, perm, false))
}

const appendFlag = os.O_WRONLY | os.O_CREATE | os.O_APPEND

// OpenAppend opens the file for a stream of appends, such as a log that
// another program writes to.
func (d *Dir) OpenAppend(name string, perm os.FileMode) (*os.File, error) {
	f, err := d.open(name, appendFlag, perm)
	return f, d.fail("open", name, err)
}

func (d *Dir) open(name string, flag int, perm os.FileMode) (*os.File, error) {
	if err := d.root.MkdirAll(filepath.Dir(name), d.dirMode); err != nil {
		return nil, err
	}
	return d.root.OpenFile(name, flag, perm)
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

// Remove and RemoveAll do nothing when the name is already gone.
func (d *Dir) Remove(name string) error {
	if err := d.root.Remove(name); !os.IsNotExist(err) {
		return d.fail("remove", name, err)
	}
	return nil
}

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
