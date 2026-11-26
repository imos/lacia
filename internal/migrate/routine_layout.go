package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/task"
)

func (p *Plan) planAreaRoutines(cfg task.Config, tracked []string) error {
	allowed := map[string]bool{}
	for _, area := range cfg.Areas {
		allowed[area] = true
		base := task.RoutineBase(task.AreaRoutineFormat, area)
		if exists(p.root, base) {
			return fmt.Errorf("%s がすでにあります。移行する前に中身を確かめてください", base)
		}
	}
	legacy, err := filepath.Glob(filepath.Join(p.root, "schedules", "*.md"))
	if err != nil {
		return err
	}
	for _, file := range legacy {
		if filepath.Base(file) != "README.md" {
			return fmt.Errorf("%s が残っています。先に固定IDの定期タスクへ移してください", file)
		}
	}
	oldBase := task.RoutineBase(cfg.Format, "")
	if info, err := os.Lstat(filepath.Join(p.root, oldBase)); err == nil && !info.IsDir() {
		return fmt.Errorf("%s は定期タスクのディレクトリーではありません", oldBase)
	}
	entries, err := os.ReadDir(filepath.Join(p.root, oldBase))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	trackedFiles := map[string]bool{}
	for _, filename := range tracked {
		trackedFiles[filename] = true
	}
	paths := map[string]string{}
	for _, entry := range entries {
		source := path.Join(oldBase, entry.Name())
		var target string
		switch {
		case entry.Name() == "migration-map.json" && entry.Type().IsRegular():
			target = "archive/routine-migration-map.json"
		case entry.IsDir() && task.ValidRoutineID(entry.Name()):
			definition := path.Join(source, "routine.md")
			if !trackedFiles[definition] {
				return fmt.Errorf("%s がGitで管理されていません。記録を確かめてから移してください", definition)
			}
			full := filepath.Join(p.root, filepath.FromSlash(definition))
			info, err := os.Lstat(full)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s は通常のファイルではありません", definition)
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			doc, err := task.ParseDoc(string(data))
			if err != nil {
				return fmt.Errorf("%s: %w", definition, err)
			}
			if doc.Get("id") != entry.Name() {
				return fmt.Errorf("%s の id がディレクトリー名と一致しません", definition)
			}
			area := doc.Get("area")
			if area == "" {
				area = cfg.Areas[0]
				p.Attention = append(p.Attention, fmt.Sprintf("%s の area が空なので、%s に移します", definition, area))
			}
			if !allowed[area] {
				return fmt.Errorf("%s の area %q は設定の areas にありません", definition, area)
			}
			target = path.Join(filepath.ToSlash(task.RoutineBase(task.AreaRoutineFormat, area)), entry.Name())
		default:
			return fmt.Errorf("%s は移行先を決められません。内容と定期タスクのIDを確かめてください", source)
		}
		if exists(p.root, target) {
			return fmt.Errorf("%s がすでにあります", target)
		}
		trackedSource := false
		for _, file := range tracked {
			if under(file, source) {
				trackedSource = true
				p.Stats.MovedFiles++
			}
		}
		p.steps = append(p.steps, step{From: source, To: target, Git: trackedSource})
		paths[source] = target
	}
	p.Stats.Moves = len(p.steps)

	for _, filename := range tracked {
		if p.archived(filename) || filepath.Ext(filename) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.root, filepath.FromSlash(filename)))
		if err != nil {
			return err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		if p.final(filename) == filename && !bytes.Contains(data, []byte("routines/")) {
			continue
		}
		rel, isAreaFile := strings.CutPrefix(filename, "areas/")
		frozen := isAreaFile && artifactOfTask(rel, allowed)
		body := p.rewrite(filename, string(data), true, !frozen, paths, nil, nil)
		if path.Base(filename) == "routine.md" && under(filename, oldBase) {
			body = dropRoutineArea(body)
		}
		if body != string(data) {
			p.writes[p.final(filename)] = []byte(body)
		}
	}

	ignore, err := os.ReadFile(filepath.Join(p.root, ".gitignore"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.writes[".gitignore"] = []byte(routineIgnore(string(ignore)))
	config, err := os.ReadFile(filepath.Join(p.root, task.ConfigFile))
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return fmt.Errorf("%s を読めません: %w", task.ConfigFile, err)
	}
	fields["format"] = json.RawMessage("2")
	next, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return fmt.Errorf("%s に format を書けません: %w", task.ConfigFile, err)
	}
	p.writes[task.ConfigFile] = append(next, '\n')
	sort.Strings(p.Attention)
	return nil
}

func dropRoutineArea(src string) string {
	lines := strings.Split(src, "\n")
	for i := 1; i < len(lines) && strings.TrimSuffix(lines[i], "\r") != "---"; i++ {
		if strings.HasPrefix(lines[i], "area:") {
			return strings.Join(append(lines[:i], lines[i+1:]...), "\n")
		}
	}
	return src
}

func routineIgnore(src string) string {
	const old = "/routines/*/files/"
	const next = "/areas/*/routines/*/files/"
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	var out []string
	has := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case old:
			continue
		case next:
			has = true
		}
		out = append(out, line)
	}
	if !has {
		out = append(out, next)
	}
	return strings.Join(out, "\n") + "\n"
}
