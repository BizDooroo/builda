package main

import (
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

const controllerStateVersion = 1

var (
	errExecutionNotFound = errors.New("execution not found")
	errExecutionActive   = errors.New("execution is not terminal")
)

// ControllerStore owns the single persisted controller snapshot. Every
// mutation is applied to a clone, persisted atomically, and only then
// committed to memory, so a persistence failure never acknowledges work.
type ControllerStore struct {
	mu         sync.Mutex
	statePath  string
	logDir     string
	maxHistory int
	data       controllerStateData
	changed    chan struct{}
}

func newControllerStore(statePath, logDir string, maxHistory int) (*ControllerStore, error) {
	return openControllerStore(statePath, logDir, maxHistory, true)
}

// openControllerStore loads the snapshot. With create set it also prepares the
// log directory; an inspection-only caller leaves the filesystem alone.
func openControllerStore(statePath, logDir string, maxHistory int, create bool) (*ControllerStore, error) {
	if create {
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return nil, err
		}
	}
	store := &ControllerStore{
		statePath:  statePath,
		logDir:     logDir,
		maxHistory: normalizeMaxHistory(maxHistory),
		data:       controllerStateData{Version: controllerStateVersion, Agents: map[string]*AgentState{}},
		changed:    make(chan struct{}),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// load reads the persisted snapshot and recovers durable log offsets from the
// log files themselves, which are the authoritative record of accepted bytes.
func (s *ControllerStore) load() error {
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var loaded controllerStateData
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse controller state %s: %w", s.statePath, err)
	}
	if loaded.Agents == nil {
		loaded.Agents = map[string]*AgentState{}
	}
	loaded.Version = controllerStateVersion
	for _, execution := range loaded.Executions {
		if execution == nil {
			continue
		}
		if size, err := executionLogSize(s.logDir, execution.ID); err == nil {
			execution.LogOffset = size
		}
	}
	backfillImportedOrigins(&loaded)
	s.data = loaded
	return nil
}

// backfillImportedOrigins reconstructs the ledger from retained executions.
// Snapshots written before the ledger existed carry the origin only on the
// execution, so this keeps a repeated import a no-op after an upgrade.
func backfillImportedOrigins(st *controllerStateData) {
	known := make(map[originKey]bool, len(st.ImportedOrigins))
	for _, origin := range st.ImportedOrigins {
		known[origin.key()] = true
	}
	for _, execution := range st.Executions {
		if execution == nil || execution.Origin == nil {
			continue
		}
		key := originKey{Machine: execution.Origin.Machine, LegacyRunID: execution.Origin.LegacyRunID}
		if key.Machine == "" || key.LegacyRunID == "" || known[key] {
			continue
		}
		known[key] = true
		st.ImportedOrigins = append(st.ImportedOrigins, ImportedOrigin{
			Machine:     key.Machine,
			LegacyRunID: key.LegacyRunID,
			ExecutionID: execution.ID,
			ImportedAt:  execution.Origin.ImportedAt,
		})
	}
}

// Subscribe returns a channel closed on the next committed mutation.
func (s *ControllerStore) Subscribe() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *ControllerStore) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// mutate applies fn to a clone of the state, persists it, and commits only on
// success. The callback must not retain pointers from the cloned state.
func (s *ControllerStore) mutate(fn func(st *controllerStateData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(fn)
}

func (s *ControllerStore) mutateLocked(fn func(st *controllerStateData) error) error {
	next := s.data.clone()
	if err := fn(&next); err != nil {
		return err
	}
	next.Sequence++
	pruned := pruneExecutions(&next, s.maxHistory)
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encode controller state: %w", err)
	}
	if err := writeFileAtomicSync(s.statePath, encoded, 0o600); err != nil {
		return fmt.Errorf("persist controller state: %w", err)
	}
	s.data = next
	s.broadcastLocked()
	for _, id := range pruned {
		removeExecutionLog(s.logDir, id)
	}
	return nil
}

// read runs fn against the live state under the store lock. The callback must
// treat the state as read-only.
func (s *ControllerStore) read(fn func(st *controllerStateData)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
}

func (s *ControllerStore) SetMaxHistory(maxHistory int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := normalizeMaxHistory(maxHistory)
	if next == s.maxHistory {
		return nil
	}
	s.maxHistory = next
	return s.mutateLocked(func(st *controllerStateData) error { return nil })
}

// Find returns a defensive copy of one execution.
func (s *ControllerStore) Find(id string) (*Execution, bool) {
	var found *Execution
	s.read(func(st *controllerStateData) {
		if execution := st.find(id); execution != nil {
			found = execution.clone()
		}
	})
	return found, found != nil
}

// Executions returns copies of all executions, newest request first.
func (s *ControllerStore) Executions() []*Execution {
	var list []*Execution
	s.read(func(st *controllerStateData) {
		list = make([]*Execution, 0, len(st.Executions))
		for _, execution := range st.Executions {
			if execution != nil {
				list = append(list, execution.clone())
			}
		}
	})
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].RequestedAt.After(list[j].RequestedAt)
	})
	return list
}

// QueuedExecutions returns queued executions in enqueue order.
func (s *ControllerStore) QueuedExecutions() []*Execution {
	var list []*Execution
	s.read(func(st *controllerStateData) {
		for _, execution := range st.Executions {
			if execution != nil && execution.Status == StatusQueued {
				list = append(list, execution.clone())
			}
		}
	})
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].RequestedAt.Before(list[j].RequestedAt)
	})
	return list
}

// AgentStates returns copies of every persisted agent scheduling state.
func (s *ControllerStore) AgentStates() map[string]*AgentState {
	states := map[string]*AgentState{}
	s.read(func(st *controllerStateData) {
		for id, state := range st.Agents {
			states[id] = state.clone()
		}
	})
	return states
}

// HasImportedOrigin reports whether a legacy run has already been imported.
// It consults the ledger, so pruning the history or deleting the run it
// produced does not make it importable again.
func (s *ControllerStore) HasImportedOrigin(machine, legacyRunID string) bool {
	found := false
	s.read(func(st *controllerStateData) {
		found = st.hasOrigin(originKey{Machine: machine, LegacyRunID: legacyRunID})
	})
	return found
}

// ImportedOrigins returns a copy of the ledger.
func (s *ControllerStore) ImportedOrigins() []ImportedOrigin {
	var origins []ImportedOrigin
	s.read(func(st *controllerStateData) {
		origins = append([]ImportedOrigin(nil), st.ImportedOrigins...)
	})
	return origins
}

// SetLogOffset records the durable log offset for display. The log file
// length is the authoritative record and is re-read on controller restart, so
// this value is persisted lazily with the next state mutation.
func (s *ControllerStore) SetLogOffset(id string, offset int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if execution := s.data.find(id); execution != nil && offset > execution.LogOffset {
		execution.LogOffset = offset
	}
}

// DeleteExecution removes one terminal execution and its log file.
func (s *ControllerStore) DeleteExecution(id string) error {
	err := s.mutate(func(st *controllerStateData) error {
		for index, execution := range st.Executions {
			if execution == nil || execution.ID != id {
				continue
			}
			if !isExecutionTerminal(execution.Status) {
				return errExecutionActive
			}
			st.Executions = append(st.Executions[:index], st.Executions[index+1:]...)
			return nil
		}
		return errExecutionNotFound
	})
	if err != nil {
		return err
	}
	removeExecutionLog(s.logDir, id)
	return nil
}

// Wait blocks until the execution reaches a terminal state.
func (s *ControllerStore) Wait(done <-chan struct{}, id string) bool {
	for {
		changed := s.Subscribe()
		execution, ok := s.Find(id)
		if !ok {
			return false
		}
		if isExecutionTerminal(execution.Status) {
			return true
		}
		select {
		case <-changed:
		case <-done:
			return false
		}
	}
}

// pruneExecutions enforces the terminal history cap. Queued and active
// executions are always retained.
func pruneExecutions(st *controllerStateData, maxHistory int) []string {
	maxHistory = normalizeMaxHistory(maxHistory)
	terminal := 0
	for _, execution := range st.Executions {
		if execution != nil && isExecutionTerminal(execution.Status) {
			terminal++
		}
	}
	remove := terminal - maxHistory
	if remove <= 0 {
		return nil
	}
	pruned := make([]string, 0, remove)
	kept := make([]*Execution, 0, len(st.Executions))
	for _, execution := range st.Executions {
		if remove > 0 && execution != nil && isExecutionTerminal(execution.Status) {
			pruned = append(pruned, execution.ID)
			remove--
			continue
		}
		kept = append(kept, execution)
	}
	st.Executions = kept
	return pruned
}

func executionLogPath(logDir, id string) string {
	return filepath.Join(logDir, filepath.Base(id)+".log")
}

func executionLogSize(logDir, id string) (int64, error) {
	info, err := os.Stat(executionLogPath(logDir, id))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func removeExecutionLog(logDir, id string) {
	path := executionLogPath(logDir, id)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove execution log %s: %v", path, err)
	}
}

// newExecutionID returns a sortable unique execution ID.
func newExecutionID() string {
	return time.Now().UTC().Format("20060102-150405") + "-" + randomHex(6)
}
