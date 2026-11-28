package lacia

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// install.sh is run against a release served from this machine, with a
// binary that only writes down how lacia setup was called.
func TestInstallScript(t *testing.T) {
	for _, tool := range []string{"sh", "curl", "awk", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not here: %v", tool, err)
		}
	}
	binary := "#!/bin/sh\necho \"$@\" \"id=$LACIA_GOOGLE_CLIENT_ID\" > \"$HOME/called\"\n"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(binary)))
	for _, c := range []struct {
		name, platform, sums, fails string
	}{
		{name: "setup", platform: "Darwin arm64", sums: sum + "  lacia-darwin-arm64\n"},
		{name: "wrong sum", platform: "Darwin arm64", sums: strings.Repeat("0", 64) + "  lacia-darwin-arm64\n", fails: "チェックサムの一覧と合いません"},
		{name: "not listed", platform: "Darwin arm64", sums: sum + "  lacia-linux-amd64\n", fails: "チェックサムの一覧と合いません"},
		{name: "no release", platform: "Linux x86_64", sums: sum + "  lacia-darwin-arm64\n", fails: "make install"},
	} {
		t.Run(c.name, func(t *testing.T) {
			release := http.NewServeMux()
			release.HandleFunc("/download/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, c.sums) })
			release.HandleFunc("/download/lacia-darwin-arm64", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, binary) })
			server := httptest.NewServer(release)
			defer server.Close()

			home, bin := t.TempDir(), t.TempDir()
			// The platform comes from uname; this one tells what the case needs.
			system, machine, _ := strings.Cut(c.platform, " ")
			uname := fmt.Sprintf("#!/bin/sh\ncase $1 in -s) echo %s ;; -m) echo %s ;; esac\n", system, machine)
			if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(uname), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "install.sh", "--repo", "https://github.com/example/records.git")
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"LACIA_DOWNLOAD_URL="+server.URL+"/download", "LACIA_GOOGLE_CLIENT_ID=id.apps.example")
			out, err := cmd.CombinedOutput()
			called, _ := os.ReadFile(filepath.Join(home, "called"))
			if c.fails != "" {
				if err == nil || !strings.Contains(string(out), c.fails) || len(called) > 0 {
					t.Errorf("err = %v, called = %q, output:\n%s", err, called, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v:\n%s", err, out)
			}
			if want := "setup --repo https://github.com/example/records.git id=id.apps.example\n"; string(called) != want {
				t.Errorf("called = %q, want %q", called, want)
			}
		})
	}
}

// The images of the install guide stay within 1 MiB in all, so that a clone
// of lacia does not grow much with them (docs/install-guide.md).
func TestGuideImagesFit(t *testing.T) {
	total := int64(0)
	err := filepath.WalkDir("docs/images", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		total += info.Size()
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if total > 1<<20 {
		t.Errorf("the images under docs/images are %d bytes, over 1 MiB", total)
	}
}
