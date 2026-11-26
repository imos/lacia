package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/task"
)

// runProject writes to a project. "note" adds an entry to its history,
// the way `lacia task note` does for a task: log.md in a repository of
// format 0, log.jsonl in one of format 1.
func runProject(args []string) error {
	if len(args) < 3 || args[0] != "note" {
		return errors.New("使い方: lacia project note <名前> \"履歴に書く文章\"（名前は、project.md のあるディレクトリーの名前か、<領域>/<名前>）")
	}
	ref, entry := args[1], strings.TrimSpace(strings.Join(args[2:], " "))
	if entry == "" {
		return errors.New("履歴に書く文章を指定してください")
	}
	dir, err := findRoot("")
	if err != nil {
		return err
	}
	actor := os.Getenv("LACIA_ACTOR")
	if api := apiForRoot(dir); api != "" {
		if err := api.call("POST", "/api/projects/notes", map[string]string{"project": ref, "log": entry, "actor": actor}, nil); err != nil {
			return err
		}
		fmt.Println("プロジェクトの履歴に書きました。")
		return nil
	}
	cfg, err := task.LoadConfig(dir)
	if err != nil {
		return err
	}
	if err := cfg.RequireCurrentFormat(); err != nil {
		return err
	}
	p, err := project.Find(dir, cfg.Format, cfg.Areas, ref)
	if err != nil {
		return err
	}
	if actor == "" {
		actor = me().Name()
	}
	e := task.Event{Type: task.EventNote, By: noteActor(actor), Text: entry}
	if err := project.AppendLog(filepath.Join(dir, filepath.FromSlash(p.Dir)), time.Now().In(cfg.Location()), e); err != nil {
		return err
	}
	fmt.Println("プロジェクトの履歴に書きました。")
	return nil
}
