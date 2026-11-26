package main

import (
	"github.com/imos/lacia/internal/task"
	"time"
)

// commandLocation uses the selected repository when a command displays times.
func commandLocation() *time.Location {
	root, err := findRoot("")
	if err == nil {
		if cfg, err := task.LoadConfig(root); err == nil {
			return cfg.Location()
		}
	}
	return task.JST
}
