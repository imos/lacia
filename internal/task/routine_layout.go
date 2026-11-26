package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const AreaRoutineFormat = 2

var routineID = regexp.MustCompile(`^S[1-9][0-9]*$`)

func ValidRoutineID(id string) bool { return routineID.MatchString(id) }

// RoutineBase returns the repository-relative directory for routine definitions.
func RoutineBase(format int, area string) string {
	if format >= AreaRoutineFormat {
		return filepath.Join(AreaDir(format, area), "routines")
	}
	return "routines"
}

// RoutineDir resolves a routine by stable ID without parsing its definition.
// Old layouts also write run history for schedule slugs and deleted jobs.
func RoutineDir(root, id string) (string, error) {
	if id == "." || id == ".." || id == "" || filepath.Base(id) != id {
		return "", fmt.Errorf("定期タスクIDが正しくありません: %q", id)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		return "", err
	}
	if cfg.Format < AreaRoutineFormat {
		return filepath.Join(root, RoutineBase(cfg.Format, ""), id), nil
	}
	if !ValidRoutineID(id) {
		return "", fmt.Errorf("定期タスクIDが正しくありません: %q", id)
	}
	var found string
	for _, area := range cfg.Areas {
		dir := filepath.Join(root, RoutineBase(cfg.Format, area), id)
		info, err := os.Lstat(filepath.Join(dir, "routine.md"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s は通常のファイルではありません", filepath.Join(dir, "routine.md"))
		}
		if found != "" && found != dir {
			return "", fmt.Errorf("定期タスクID %s が複数の領域にあります", id)
		}
		found = dir
	}
	if found == "" {
		return "", fmt.Errorf("定期タスク %s が見つかりません", id)
	}
	return found, nil
}
