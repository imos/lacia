package task

import (
	"fmt"
	"path/filepath"
)

// ReadLegacyTasks inspects format 0 solely for migration and diagnosis.
// It returns values, not a store that could write to an old repository.
func ReadLegacyTasks(root string, areas []string) ([]Task, []Problem, error) {
	var tasks []Task
	var problems []Problem
	seen := map[string]string{}
	reader := Store{root: root}
	for _, area := range areas {
		paths, err := filepath.Glob(filepath.Join(root, area, "tasks", "*", "*", "task.md"))
		if err != nil {
			return nil, nil, err
		}
		for _, path := range paths {
			rel, _ := filepath.Rel(root, path)
			t, err := reader.read(area, filepath.ToSlash(filepath.Dir(rel)))
			switch {
			case err != nil:
				problems = append(problems, Problem{rel, err.Error()})
			case seen[t.ID] != "":
				problems = append(problems, Problem{rel, fmt.Sprintf("ID %s は %s でも使われています", t.ID, seen[t.ID])})
			default:
				seen[t.ID] = t.Dir
				tasks = append(tasks, *t)
			}
		}
	}
	return tasks, problems, nil
}
