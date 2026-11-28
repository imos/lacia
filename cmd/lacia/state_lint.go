package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

// stateLintContext reads only an explicitly selected server state directory.
// Root and API settings do not identify the state belonging to that repository.
func stateLintContext(state string) (task.StateContext, error) {
	context := task.StateContext{Now: time.Now(), StaleAfter: caller.Active}
	if state == "" {
		return context, nil
	}
	path := filepath.Join(state, "sessions.json")
	if _, err := os.Stat(path); err != nil {
		return context, fmt.Errorf("会話の最終利用の記録を読めません: %w", err)
	}
	dir, err := disk.OpenTools(state)
	if err != nil {
		return context, fmt.Errorf("会話の最終利用の記録を読めません: %w", err)
	}
	defer dir.Close()
	registry, err := caller.OpenRegistry(dir, "sessions.json")
	if err != nil {
		return context, fmt.Errorf("会話の最終利用の記録を読めません: %w", err)
	}
	context.OwnerLast = map[string]time.Time{}
	for _, session := range registry.List() {
		context.OwnerLast[session.Key] = session.Last
	}
	return context, nil
}

func lintWaitingConditions(api apiClient, state string, targets []task.Task) (map[string]string, error) {
	warnings := make(map[string]string)
	if state == "" && api != "" {
		if !slices.ContainsFunc(targets, func(t task.Task) bool { return t.Status == task.Waiting }) {
			return nil, nil
		}
		err := api.readTask("/api/tasks/waiting-warnings", &warnings)
		return warnings, err
	}
	if !slices.ContainsFunc(targets, func(t task.Task) bool { return t.MissingWaitingCondition(false) }) {
		return nil, nil
	}
	live, err := localWaitConditions(state, true)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		if t.MissingWaitingCondition(live[t.ID]) {
			warnings[t.ID] = task.WaitingConditionWarning
		}
	}
	return warnings, nil
}

// A standalone CLI checks the runtime records only when given their location.
// A request in the queue hides the warning in a list, but it is no reason
// to let a task start waiting (withRequests false): see Server.liveCondition.
func localWaitConditions(state string, withRequests bool) (map[string]bool, error) {
	if state == "" {
		return nil, nil
	}
	requests, err := openExistingState(state, "requests", request.OpenRecords)
	if err != nil {
		return nil, err
	}
	if !withRequests {
		requests = nil
	}
	watches, err := openExistingState(state, "waits", wait.Open)
	if err != nil {
		return nil, err
	}
	handovers, err := openExistingState(state, "handovers", handover.Open)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool)
	for id, activity := range requests.TaskActivity() {
		if activity == request.Queued || activity == request.Running {
			live[id] = true
		}
	}
	for id := range watches.OpenByTask() {
		live[id] = true
	}
	for id := range handovers.ConditionsByTask() {
		live[id] = true
	}
	return live, nil
}

func openExistingState[T any](state, name string, open func(*disk.Dir) (*T, error)) (*T, error) {
	path := filepath.Join(state, name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	dir, err := disk.OpenTools(path)
	if err != nil {
		return nil, err
	}
	return open(dir)
}
