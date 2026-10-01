package main

import (
	"context"
	"log"
	"os"
	"sync"
	"time"
)

// Agent runs one job at a time on its host. It owns a durable journal so a
// restart can never re-execute an incomplete run, and it keeps heartbeats
// flowing on their own goroutine while a script runs, uploads, or polls.
type Agent struct {
	runtime AgentRuntime
	client  *agentClient
	journal *agentJournal

	mu        sync.Mutex
	current   string
	state     string
	cancel    chan struct{}
	cancelled bool
	blocked   map[string]bool
	acked     map[string]int64
}

func newAgent(runtime AgentRuntime, token string) (*Agent, error) {
	if err := os.MkdirAll(runtime.SpoolDir, 0o700); err != nil {
		return nil, err
	}
	journal, err := newAgentJournal(runtime.ExecDir)
	if err != nil {
		return nil, err
	}
	return &Agent{
		runtime: runtime,
		client:  newAgentClient(runtime, token),
		journal: journal,
		state:   agentStateIdle,
		blocked: map[string]bool{},
		acked:   map[string]int64{},
	}, nil
}

// Run reconciles anything left by a previous process and then serves the
// controller until the context is canceled.
func (a *Agent) Run(ctx context.Context) error {
	go a.heartbeatLoop(ctx)
	if err := a.recover(ctx); err != nil {
		log.Printf("recover agent journal: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if a.isBlocked() {
			a.waitBlocked(ctx)
			continue
		}
		response, err := a.client.Poll(ctx, a.currentExecution(), a.journal.IDs())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("poll controller: %v", err)
			if !sleepContext(ctx, 2*time.Second) {
				return ctx.Err()
			}
			continue
		}
		if response.Assignment == nil {
			continue
		}
		a.handleAssignment(ctx, *response.Assignment)
	}
}

// handleAssignment journals the accepted assignment before anything else, then
// asks the controller for permission to start.
func (a *Agent) handleAssignment(ctx context.Context, assignment AgentAssignment) {
	entry, err := a.journal.Load(assignment.ExecutionID)
	if err != nil {
		entry = &journalEntry{
			ExecutionID: assignment.ExecutionID,
			Phase:       journalAccepted,
			Assignment:  assignment,
			AcceptedAt:  time.Now(),
			ExitCode:    -1,
		}
		if err := a.journal.Save(entry); err != nil {
			log.Printf("journal assignment %s: %v", assignment.ExecutionID, err)
			return
		}
	}
	if entry.Phase == journalStarted || entry.Phase == journalFinished || entry.Phase == journalReported {
		// A duplicate delivery of an assignment that already ran must never
		// start a second process.
		log.Printf("ignoring duplicate assignment %s already at phase %s", entry.ExecutionID, entry.Phase)
		return
	}
	a.runAssignment(ctx, entry)
}

// runAssignment obtains the single start permit and executes.
func (a *Agent) runAssignment(ctx context.Context, entry *journalEntry) {
	a.beginExecution(entry.ExecutionID)
	defer a.endExecution()

	if entry.Phase != journalPermitted {
		permit, err := a.client.Permit(ctx, entry.ExecutionID)
		if err != nil {
			log.Printf("request start permit for %s: %v", entry.ExecutionID, err)
			return
		}
		if !permit.Granted {
			entry.Phase = journalFinished
			entry.Status = StatusCanceled
			entry.FinishedAt = time.Now()
			entry.Error = permit.Reason
			entry.LogLength = a.localLogSize(entry.ExecutionID)
			if err := a.journal.Save(entry); err != nil {
				log.Printf("journal denied permit %s: %v", entry.ExecutionID, err)
				return
			}
			a.completeExecution(ctx, entry)
			return
		}
		entry.Phase = journalPermitted
		entry.PermittedAt = time.Now()
		if err := a.journal.Save(entry); err != nil {
			log.Printf("journal start permit %s: %v", entry.ExecutionID, err)
			return
		}
	}

	a.setState(agentStateRunning)
	uploadCtx, stopUpload := context.WithCancel(context.Background())
	var uploader sync.WaitGroup
	uploader.Add(1)
	go func() {
		defer uploader.Done()
		a.streamLog(uploadCtx, entry.ExecutionID)
	}()

	outcome := a.runExecution(ctx, entry, a.cancelChannel())
	stopUpload()
	uploader.Wait()

	entry.Phase = journalFinished
	entry.Status = outcome.Status
	entry.ExitCode = outcome.ExitCode
	entry.Error = outcome.Error
	entry.FailureReason = outcome.FailureReason
	entry.FinishedAt = time.Now()
	entry.LogLength = a.localLogSize(entry.ExecutionID)
	if err := a.journal.Save(entry); err != nil {
		log.Printf("journal result %s: %v", entry.ExecutionID, err)
		return
	}
	a.completeExecution(ctx, entry)
}

// beginExecution installs the cancellation channel for the active execution.
func (a *Agent) beginExecution(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = id
	a.state = agentStateAccepted
	a.cancel = make(chan struct{})
	a.cancelled = false
}

func (a *Agent) endExecution() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = ""
	a.state = agentStateIdle
	a.cancel = nil
	a.cancelled = false
}

func (a *Agent) cancelChannel() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancel
}

// requestCancel closes the cancellation channel exactly once.
func (a *Agent) requestCancel(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != id || a.cancel == nil || a.cancelled {
		return
	}
	a.cancelled = true
	close(a.cancel)
}

func (a *Agent) currentExecution() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

func (a *Agent) setState(state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = state
}

func (a *Agent) snapshot() (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current, a.state
}

func (a *Agent) markBlocked(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.blocked[id] = true
	a.state = agentStateBlocked
}

func (a *Agent) clearBlocked(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.blocked, id)
	if len(a.blocked) == 0 && a.state == agentStateBlocked {
		a.state = agentStateIdle
	}
}

func (a *Agent) isBlocked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.blocked) > 0
}

func (a *Agent) blockedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.blocked))
	for id := range a.blocked {
		ids = append(ids, id)
	}
	return ids
}

// waitBlocked keeps the agent online but idle until an operator resolves the
// execution the agent could not account for.
func (a *Agent) waitBlocked(ctx context.Context) {
	for _, id := range a.blockedIDs() {
		response, err := a.client.Heartbeat(ctx, id, agentStateBlocked)
		if err != nil {
			continue
		}
		if !response.Known || isExecutionTerminal(response.Status) {
			a.clearBlocked(id)
			if err := a.journal.Remove(id); err != nil {
				log.Printf("remove resolved execution %s: %v", id, err)
			}
		}
	}
	sleepContext(ctx, a.runtime.HeartbeatInterval)
}

// heartbeatLoop runs independently of execution, upload, and polling so the
// controller keeps seeing the agent while a long build is in progress.
func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.runtime.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		current, state := a.snapshot()
		response, err := a.client.Heartbeat(ctx, current, state)
		if err != nil {
			continue
		}
		if current != "" && response.CancelRequested {
			a.requestCancel(current)
		}
	}
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
