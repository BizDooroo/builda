package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Journal phases record exactly how far an execution progressed on the agent.
const (
	journalAccepted  = "accepted"
	journalPermitted = "permitted"
	journalStarted   = "started"
	journalFinished  = "finished"
	journalReported  = "reported"
	journalBlocked   = "blocked"
)

// journalEntry is the agent's durable record of one execution. It is written
// before the assignment is acknowledged and updated at every transition, so an
// agent restart can never re-execute an incomplete run.
type journalEntry struct {
	ExecutionID       string          `json:"execution_id"`
	Phase             string          `json:"phase"`
	Assignment        AgentAssignment `json:"assignment"`
	AcceptedAt        time.Time       `json:"accepted_at"`
	PermittedAt       time.Time       `json:"permitted_at,omitempty"`
	StartedAt         time.Time       `json:"started_at,omitempty"`
	FinishedAt        time.Time       `json:"finished_at,omitempty"`
	PID               int             `json:"pid,omitempty"`
	PGID              int             `json:"pgid,omitempty"`
	ProcessToken      string          `json:"process_token,omitempty"`
	ProcessTokenError string          `json:"process_token_error,omitempty"`
	Status            string          `json:"status,omitempty"`
	ExitCode          int             `json:"exit_code"`
	Error             string          `json:"error,omitempty"`
	FailureReason     string          `json:"failure_reason,omitempty"`
	LogLength         int64           `json:"log_length"`
	ResultAcked       bool            `json:"result_acked,omitempty"`
	Attention         string          `json:"attention,omitempty"`
}

// agentJournal owns the agent spool layout: one directory per execution
// holding the journal file and the local log.
type agentJournal struct {
	dir string
}

func newAgentJournal(dir string) (*agentJournal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &agentJournal{dir: dir}, nil
}

func (j *agentJournal) entryDir(id string) string {
	return filepath.Join(j.dir, filepath.Base(id))
}

func (j *agentJournal) entryPath(id string) string {
	return filepath.Join(j.entryDir(id), "journal.json")
}

// LogPath is the agent-local log file written before any upload is attempted.
func (j *agentJournal) LogPath(id string) string {
	return filepath.Join(j.entryDir(id), "run.log")
}

// Save durably records the entry before the agent acts on it.
func (j *agentJournal) Save(entry *journalEntry) error {
	if entry == nil || entry.ExecutionID == "" {
		return errors.New("journal entry requires an execution id")
	}
	if err := os.MkdirAll(j.entryDir(entry.ExecutionID), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicSync(j.entryPath(entry.ExecutionID), data, 0o600)
}

func (j *agentJournal) Load(id string) (*journalEntry, error) {
	data, err := os.ReadFile(j.entryPath(id))
	if err != nil {
		return nil, err
	}
	var entry journalEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

// List returns every journal entry still on disk, oldest acceptance first.
func (j *agentJournal) List() ([]*journalEntry, error) {
	items, err := os.ReadDir(j.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries := make([]*journalEntry, 0, len(items))
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		entry, err := j.Load(item.Name())
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(a, b int) bool {
		return entries[a].AcceptedAt.Before(entries[b].AcceptedAt)
	})
	return entries, nil
}

// Remove deletes an execution's spool directory. It is only called after the
// controller has acknowledged both the complete log and the final result.
func (j *agentJournal) Remove(id string) error {
	return os.RemoveAll(j.entryDir(id))
}

// IDs returns the execution IDs the agent still tracks. The controller uses
// this to reconcile without ever reassigning an uncertain execution.
func (j *agentJournal) IDs() []string {
	entries, err := j.List()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ExecutionID)
	}
	return ids
}
