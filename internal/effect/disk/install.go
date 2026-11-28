package disk

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// These are the only files lacia writes outside the records and the tools
// directory, each at a path fixed here: the definition that keeps the
// server running, and the link that puts lacia in PATH.

// InstallLaunchAgent writes ~/Library/LaunchAgents/<label>.plist.
func InstallLaunchAgent(label string, plist []byte) error {
	dir, name, err := launchAgent(label)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.WriteFile(name, plist, 0o644)
}

func RemoveLaunchAgent(label string) error {
	dir, name, err := launchAgent(label)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Remove(name)
}

func launchAgent(label string) (*Dir, string, error) {
	if label == "" || strings.ContainsAny(label, `/\`) || strings.HasPrefix(label, ".") {
		return nil, "", fmt.Errorf("常駐の名前 %q は、ファイルの名前に使えません", label)
	}
	dir, err := homeDir("Library/LaunchAgents", 0o755)
	return dir, label + ".plist", err
}

// LinkCommand makes ~/.local/bin/lacia a symbolic link to target, replacing
// what was there in one step.
func LinkCommand(target string) error {
	dir, err := homeDir(".local/bin", 0o755)
	if err != nil {
		return err
	}
	defer dir.Close()
	tmp := ".lacia.tmp-" + rand.Text()
	err = dir.root.Symlink(target, tmp)
	if err == nil {
		err = dir.root.Rename(tmp, "lacia")
	}
	if err != nil {
		dir.root.Remove(tmp)
	}
	return dir.fail("link", "lacia", err)
}

// UnlinkCommand removes ~/.local/bin/lacia only when it is a link: a file
// there was put by someone else.
func UnlinkCommand() error {
	dir, err := homeDir(".local/bin", 0o755)
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.root.Lstat("lacia")
	if errors.Is(err, os.ErrNotExist) || err == nil && info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return dir.Remove("lacia")
}

func homeDir(name string, mode os.FileMode) (*Dir, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, name)
	if err := os.MkdirAll(path, mode); err != nil {
		return nil, err
	}
	return open(path, mode)
}
