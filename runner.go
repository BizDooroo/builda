package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	errRunActive   = errors.New("cannot delete active run")
	errRunNotFound = errors.New("run not found")
)

func cloneInputs(inputs map[string]string) map[string]string {
	if len(inputs) == 0 {
		return nil
	}
	clone := make(map[string]string, len(inputs))
	for key, value := range inputs {
		clone[key] = value
	}
	return clone
}

func inputEnv(inputs map[string]string) []string {
	env := make([]string, 0, len(inputs))
	for key, value := range inputs {
		env = append(env, taskInputEnvName(key)+"="+value)
	}
	sort.Strings(env)
	return env
}

func taskEnvironment(inputs map[string]string) []string {
	return append(os.Environ(), inputEnv(inputs)...)
}

func NewRunner(logDir string, maxHistory ...int) *Runner {
	r := &Runner{
		logDir:     logDir,
		statePath:  filepath.Join(logDir, "runs.json"),
		maxHistory: defaultMaxHistory,
		byID:       map[string]*Run{},
	}
	if len(maxHistory) > 0 {
		r.maxHistory = normalizeMaxHistory(maxHistory[0])
	}
	if err := r.loadState(); err != nil {
		log.Printf("load run state: %v", err)
	}
	r.mu.Lock()
	r.dispatchLocked()
	r.mu.Unlock()
	return r
}

func normalizeMaxHistory(maxHistory int) int {
	if maxHistory <= 0 {
		return defaultMaxHistory
	}
	return maxHistory
}

func (r *Runner) SetMaxHistory(maxHistory int) {
	r.mu.Lock()
	next := normalizeMaxHistory(maxHistory)
	if r.maxHistory == next {
		r.mu.Unlock()
		return
	}
	r.maxHistory = next
	r.saveLocked()
	r.mu.Unlock()
}

func (r *Runner) Start(task TaskConfig, inputs map[string]string) (*Run, error) {
	timeout := time.Duration(0)
	if task.Timeout != "" {
		parsed, err := time.ParseDuration(task.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout for %s: %w", task.ID, err)
		}
		timeout = parsed
	}

	id := newRunID()
	run := &Run{
		ID:           id,
		TaskID:       task.ID,
		TaskName:     task.Name,
		Script:       task.Script,
		Inputs:       cloneInputs(inputs),
		TaskSnapshot: task,
		LogPath:      filepath.Join(r.logDir, id+".log"),
		Timeout:      timeout,
		TimeoutText:  task.Timeout,
		Status:       StatusQueued,
		RequestedAt:  time.Now(),
		ExitCode:     -1,
		done:         make(chan struct{}),
	}

	r.mu.Lock()
	r.runs = append(r.runs, run)
	r.byID[run.ID] = run
	r.saveLocked()
	r.dispatchLocked()
	r.mu.Unlock()
	return run, nil
}

func (r *Runner) loadState() error {
	data, err := os.ReadFile(r.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var runs []*Run
	if err := json.Unmarshal(data, &runs); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range runs {
		if run == nil || run.ID == "" {
			continue
		}
		if run.TaskSnapshot.ID == "" {
			run.TaskSnapshot = TaskConfig{
				ID:      run.TaskID,
				Name:    run.TaskName,
				Script:  run.Script,
				Timeout: run.TimeoutText,
			}
		}
		run.cancel = nil
		run.done = make(chan struct{})
		run.doneClose = sync.Once{}
		if run.ExitCode == 0 && !isTerminal(run.Status) {
			run.ExitCode = -1
		}
		if run.Status == StatusRunning {
			run.Status = StatusAborted
			run.Error = "program restarted while run was in progress"
			run.FinishedAt = time.Now()
			run.closeDone()
		} else if isTerminal(run.Status) {
			run.closeDone()
		}
		r.runs = append(r.runs, run)
		r.byID[run.ID] = run
	}
	r.saveLocked()
	return nil
}

func (r *Runner) saveLocked() {
	pruned := r.pruneHistoryLocked()
	data, err := json.MarshalIndent(r.runs, "", "  ")
	if err != nil {
		log.Printf("marshal run state: %v", err)
		return
	}
	if err := writeFileAtomic(r.statePath, data, 0644); err != nil {
		log.Printf("write run state: %v", err)
		return
	}
	r.removePrunedLogs(pruned)
}

func (r *Runner) pruneHistoryLocked() []*Run {
	maxHistory := normalizeMaxHistory(r.maxHistory)
	r.maxHistory = maxHistory

	terminalCount := 0
	for _, run := range r.runs {
		if run != nil && isTerminal(run.Status) {
			terminalCount++
		}
	}
	removeCount := terminalCount - maxHistory
	if removeCount <= 0 {
		return nil
	}

	pruned := make([]*Run, 0, removeCount)
	kept := r.runs[:0]
	for _, run := range r.runs {
		if removeCount > 0 && run != nil && isTerminal(run.Status) {
			pruned = append(pruned, run)
			delete(r.byID, run.ID)
			removeCount--
			continue
		}
		kept = append(kept, run)
	}
	for i := len(kept); i < len(r.runs); i++ {
		r.runs[i] = nil
	}
	r.runs = kept
	return pruned
}

func (r *Runner) removePrunedLogs(pruned []*Run) {
	for _, run := range pruned {
		if run == nil || run.ID == "" {
			continue
		}
		path := filepath.Join(r.logDir, filepath.Base(run.ID)+".log")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("remove pruned run log %s: %v", path, err)
		}
	}
}

func (r *Runner) Delete(id string) error {
	var deleted *Run
	r.mu.Lock()
	for index, run := range r.runs {
		if run == nil || run.ID != id {
			continue
		}
		if !isTerminal(run.Status) {
			r.mu.Unlock()
			return errRunActive
		}
		deleted = run
		copy(r.runs[index:], r.runs[index+1:])
		r.runs[len(r.runs)-1] = nil
		r.runs = r.runs[:len(r.runs)-1]
		delete(r.byID, id)
		r.saveLocked()
		r.mu.Unlock()
		r.removePrunedLogs([]*Run{deleted})
		return nil
	}
	r.mu.Unlock()
	return errRunNotFound
}

func (r *Runner) finish(id string, canceled bool, exitCode int, errText string) {
	r.mu.Lock()
	run := r.byID[id]
	if run == nil {
		r.mu.Unlock()
		return
	}
	run.ExitCode = exitCode
	run.Error = errText
	run.FinishedAt = time.Now()
	if canceled || !run.CanceledAt.IsZero() {
		run.Status = StatusCanceled
		if run.CanceledAt.IsZero() {
			run.CanceledAt = run.FinishedAt
		}
	} else if exitCode == 0 {
		run.Status = StatusSuccess
	} else {
		run.Status = StatusFailed
	}
	if r.activeID == id {
		r.activeID = ""
	}
	run.cancel = nil
	// Persist before releasing waiters so anything that observes a terminal
	// run also observes the persisted state.
	r.saveLocked()
	run.closeDone()
	r.dispatchLocked()
	r.mu.Unlock()
}

func (r *Runner) Cancel(id string) bool {
	var cancel context.CancelFunc
	r.mu.Lock()
	run := r.byID[id]
	if run == nil {
		r.mu.Unlock()
		return false
	}
	switch run.Status {
	case StatusQueued:
		now := time.Now()
		run.Status = StatusCanceled
		run.CanceledAt = now
		run.FinishedAt = now
		run.Error = "canceled before start"
		run.closeDone()
	case StatusRunning:
		if run.CanceledAt.IsZero() {
			run.CanceledAt = time.Now()
		}
		cancel = run.cancel
	default:
		r.mu.Unlock()
		return false
	}
	r.saveLocked()
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (r *Runner) Snapshot() []*RunSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.snapshotLocked("")
}

func (r *Runner) SnapshotByTask(taskID string) []*RunSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.snapshotLocked(taskID)
}

func (r *Runner) snapshotLocked(taskID string) []*RunSummary {
	runs := make([]*RunSummary, 0, len(r.runs))
	for _, run := range r.runs {
		if taskID != "" && run.TaskID != taskID {
			continue
		}
		runs = append(runs, run.summaryLocked())
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].RequestedAt.After(runs[j].RequestedAt)
	})
	return runs
}

func (r *Runner) Find(id string) (*RunSummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run := r.byID[id]
	if run == nil {
		return nil, false
	}
	return run.summaryLocked(), true
}

func (r *Runner) Wait(ctx context.Context, id string) bool {
	r.mu.RLock()
	run := r.byID[id]
	if run == nil {
		r.mu.RUnlock()
		return false
	}
	done := run.done
	r.mu.RUnlock()

	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Run) summaryLocked() *RunSummary {
	return &RunSummary{
		ID:           r.ID,
		TaskID:       r.TaskID,
		TaskName:     r.TaskName,
		Script:       r.Script,
		Inputs:       cloneInputs(r.Inputs),
		TaskSnapshot: r.TaskSnapshot,
		LogPath:      r.LogPath,
		TimeoutText:  r.TimeoutText,
		Status:       r.Status,
		RequestedAt:  r.RequestedAt,
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
		CanceledAt:   r.CanceledAt,
		ExitCode:     r.ExitCode,
		Error:        r.Error,
	}
}

func (r *Run) closeDone() {
	r.doneClose.Do(func() {
		if r.done != nil {
			close(r.done)
		}
	})
}

func isTerminal(status string) bool {
	switch status {
	case StatusSuccess, StatusFailed, StatusCanceled, StatusAborted:
		return true
	default:
		return false
	}
}
