package disk

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// These are the only places lacia writes outside the records and the tools
// directory:
//
//   - the definition that keeps the server running, and the link that puts
//     lacia in PATH, each at a path fixed here;
//   - a directory in the system's temporary directory for what lasts one
//     conversation (TempDir), or for the socket of a Codex whose state
//     directory belongs to another user (SocketDir);
//   - the homes and work directories of the logins that the owner names in
//     the settings (MakeLoginDir and the two after it);
//   - the directory of the menu-bar app, where lacia agent keeps its state
//     (OpenNotifierState);
//   - the directory the owner names for lacia shims (OpenShimsDir).

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

// TempDir makes a new directory in the system's temporary directory, for
// what lasts one conversation; only the owner may read it.
func TempDir(prefix string) (string, error) { return os.MkdirTemp("", prefix+"*") }

// OpenTempDir opens a directory that TempDir made, by its name.
func OpenTempDir(name string) (*Dir, error) {
	if name != filepath.Base(name) || name == "." || name == ".." {
		return nil, fmt.Errorf("%s は一時ディレクトリーの中の名前ではありません", name)
	}
	return open(filepath.Join(os.TempDir(), name), 0o700)
}

// RemoveTempDir removes a directory that TempDir made, and nothing else.
func RemoveTempDir(path string) error {
	if filepath.Dir(path) != filepath.Clean(os.TempDir()) {
		return fmt.Errorf("%s は一時ディレクトリーの中にないので、消しません", path)
	}
	return os.RemoveAll(path)
}

// SocketDir opens the directory of that name in the system's temporary
// directory, making it when missing. The name stays the same across
// restarts, so that a server finds the program it left running.
func SocketDir(name string) (*Dir, error) {
	if name != filepath.Base(name) {
		return nil, fmt.Errorf("%s は一時ディレクトリーの中の名前ではありません", name)
	}
	path := filepath.Join(os.TempDir(), name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return open(path, 0o700)
}

// OpenNotifierState opens the directory the menu-bar app gives lacia agent
// for its state, making it when missing; only the owner may read it.
func OpenNotifierState(path string) (*Dir, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return open(path, 0o700)
}

// OpenShimsDir opens the directory the owner names for the commands that
// lacia shims writes, making it when missing.
func OpenShimsDir(path string) (*Dir, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	return open(path, 0o755)
}

// MakeLoginDir makes the directory at path, with missing parents, for a
// login the owner named. A directory already there is left as it is.
func MakeLoginDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// WriteLoginConfig writes the settings of a login the owner named, which
// must not exist yet.
func WriteLoginConfig(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	d, err := open(filepath.Dir(path), 0o700)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.CreateNew(filepath.Base(path), data, 0o600)
}

// LinkLoginGit makes path, in the home of a login the owner named, a
// symbolic link to target. It does not replace what is already at path.
func LinkLoginGit(target, path string) error {
	d, err := open(filepath.Dir(path), 0o700)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.fail("link", filepath.Base(path), d.root.Symlink(target, filepath.Base(path)))
}
