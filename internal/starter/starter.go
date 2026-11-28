// Package starter holds the first content of new records: the files that
// lacia setup writes into an owner's empty repository. They were the
// template repository before; the binary carries them now, so that the
// owner's records hold no tools.
//
// The files under files/ are under the MIT No Attribution license (LICENSE
// beside this file), not the license of the rest of lacia: what lacia
// writes into an owner's records is theirs to use as they like, with no
// notice to keep.
package starter

import (
	"embed"
	"encoding/json"
	"io/fs"
	"strings"

	"github.com/imos/lacia/internal/effect/disk"
)

//go:embed all:files
var files embed.FS

// placeholder is the name the files call the owner by until one is set.
const placeholder = "オーナーさん"

// profileName is the line of memory/profile.md that carries the name.
const profileName = "- 呼び名："

// Write puts the files into the records, calling the owner by name. A file
// already there is replaced.
func Write(records *disk.Dir, name string) error {
	return fs.WalkDir(files, "files", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, _ := files.ReadFile(path)
		rel := strings.TrimPrefix(path, "files/")
		text := string(data)
		switch rel {
		case ".lacia/config.json":
			quoted, _ := json.Marshal(name)
			text = strings.Replace(text, `"user": "`+placeholder+`"`, `"user": `+string(quoted), 1)
		case "memory/profile.md":
			text = strings.Replace(text, "\n"+profileName+"\n", "\n"+profileName+name+"\n", 1)
		}
		return records.WriteFile(rel, []byte(text), 0o644)
	})
}
