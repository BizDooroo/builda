package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"
)

const (
	logFlushInterval = time.Second
	resultRetryDelay = 2 * time.Second
	orphanKillGrace  = 10 * time.Second
)

func (a *Agent) localLogSize(id string) int64 {
	info, err := os.Stat(a.journal.LogPath(id))
	if err != nil {
		return 0
	}
	return info.Size()
}

// streamLog uploads new log bytes while the script runs so operators can
// follow a build live. The local file is always written first.
func (a *Agent) streamLog(ctx context.Context, id string) {
	ticker := time.NewTicker(logFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := a.flushLog(context.Background(), id); err != nil {
			log.Printf("upload log for %s: %v", id, err)
		}
	}
}

// flushLog uploads every durable local byte the controller has not yet
// acknowledged. Chunks are ordered by byte offset, duplicates are idempotent,
// and a controller that reports a lower offset causes retransmission.
func (a *Agent) flushLog(ctx context.Context, id string) (int64, error) {
	path := a.journal.LogPath(id)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer file.Close()

	acked := a.ackedOffset(id)
	for attempt := 0; attempt < 10000; attempt++ {
		info, err := file.Stat()
		if err != nil {
			return acked, err
		}
		size := info.Size()
		if acked >= size {
			a.setAckedOffset(id, acked)
			return acked, nil
		}
		length := size - acked
		if length > agentLogChunkLimit {
			length = agentLogChunkLimit
		}
		buf := make([]byte, length)
		read, err := file.ReadAt(buf, acked)
		if read == 0 {
			if err != nil {
				return acked, err
			}
			return acked, nil
		}
		next, err := a.client.UploadLog(ctx, id, acked, buf[:read])
		if err != nil {
			return acked, err
		}
		if next == acked {
			// The controller accepted nothing new; stop instead of spinning.
			a.setAckedOffset(id, acked)
			return acked, nil
		}
		acked = next
		a.setAckedOffset(id, acked)
	}
	return acked, errors.New("log upload did not converge")
}

func (a *Agent) ackedOffset(id string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.acked == nil {
		return 0
	}
	return a.acked[id]
}

func (a *Agent) setAckedOffset(id string, offset int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.acked == nil {
		a.acked = map[string]int64{}
	}
	a.acked[id] = offset
}

// completeExecution retains the local log and result until the controller has
// acknowledged every byte and accepted the final status. A transient failure
// is retried indefinitely, because the result is the only record of what
// happened. A refusal that retrying cannot fix is escalated instead, so the
// agent never spins on it and never silently drops the run.
func (a *Agent) completeExecution(ctx context.Context, entry *journalEntry) {
	a.setState(agentStateUploading)
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := a.flushLog(ctx, entry.ExecutionID); err != nil {
			if isPermanentAgentError(err) {
				a.abandonExecution(ctx, entry, "the controller refused the log upload", err)
				return
			}
			log.Printf("flush log for %s: %v", entry.ExecutionID, err)
			if !sleepContext(ctx, resultRetryDelay) {
				return
			}
			continue
		}
		response, err := a.client.SubmitResult(ctx, AgentResultRequest{
			ExecutionID:   entry.ExecutionID,
			Status:        entry.Status,
			ExitCode:      entry.ExitCode,
			Error:         entry.Error,
			FailureReason: entry.FailureReason,
			LogLength:     entry.LogLength,
		})
		if err != nil {
			if isPermanentAgentError(err) {
				a.abandonExecution(ctx, entry, "the controller refused the result", err)
				return
			}
			log.Printf("submit result for %s: %v", entry.ExecutionID, err)
			if !sleepContext(ctx, resultRetryDelay) {
				return
			}
			continue
		}
		if response.Accepted {
			break
		}
		// The controller is missing log bytes; resend from its durable offset.
		a.setAckedOffset(entry.ExecutionID, response.AckOffset)
		if !sleepContext(ctx, resultRetryDelay) {
			return
		}
	}
	entry.Phase = journalReported
	entry.ResultAcked = true
	if err := a.journal.Save(entry); err != nil {
		log.Printf("journal reported result %s: %v", entry.ExecutionID, err)
		return
	}
	if err := a.journal.Remove(entry.ExecutionID); err != nil {
		log.Printf("remove spool for %s: %v", entry.ExecutionID, err)
	}
	a.setState(agentStateIdle)
}

// abandonExecution stops reporting an execution the controller will never
// accept. The outcome is still on disk, so the spool is kept and the agent
// blocks for an operator rather than looping or pretending the run is done.
func (a *Agent) abandonExecution(ctx context.Context, entry *journalEntry, reason string, cause error) {
	details := fmt.Sprintf("%s: %v; the result stays in the agent spool at %s",
		reason, cause, a.journal.entryDir(entry.ExecutionID))
	log.Printf("execution %s needs attention: %s", entry.ExecutionID, details)
	entry.Phase = journalBlocked
	entry.Attention = details
	if err := a.journal.Save(entry); err != nil {
		log.Printf("journal blocked execution %s: %v", entry.ExecutionID, err)
	}
	// Reporting may itself be refused; the local block stands either way.
	if err := a.client.ReportAttention(ctx, entry.ExecutionID, details); err != nil {
		log.Printf("report attention for %s: %v", entry.ExecutionID, err)
	}
	a.markBlocked(entry.ExecutionID)
	a.setState(agentStateBlocked)
}

// recover reconciles everything a previous agent process left behind. An
// incomplete run is never re-executed: it is either proven finished and
// reported as ABORTED, or escalated for operator attention.
func (a *Agent) recover(ctx context.Context) error {
	entries, err := a.journal.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Phase {
		case journalReported:
			if err := a.journal.Remove(entry.ExecutionID); err != nil {
				log.Printf("remove reported execution %s: %v", entry.ExecutionID, err)
			}
		case journalFinished:
			a.completeExecution(ctx, entry)
		case journalStarted:
			a.recoverStarted(ctx, entry)
		case journalBlocked:
			a.markBlocked(entry.ExecutionID)
		case journalAccepted, journalPermitted:
			a.runAssignment(ctx, entry)
		}
	}
	return nil
}

// recoverStarted decides the fate of a process group started before the agent
// restarted. It never kills a process it cannot prove it owns.
func (a *Agent) recoverStarted(ctx context.Context, entry *journalEntry) {
	verdict := verifyOrphan(entry)
	if !verdict.Ended && verdict.Owned {
		_ = killProcessGroup(entry.PGID, syscall.SIGKILL)
		deadline := time.Now().Add(orphanKillGrace)
		for time.Now().Before(deadline) {
			if !sleepContext(ctx, 250*time.Millisecond) {
				return
			}
			verdict = verifyOrphan(entry)
			if verdict.Ended {
				break
			}
		}
	}
	if !verdict.Ended {
		a.blockExecution(ctx, entry, verdict.Details)
		return
	}
	entry.Phase = journalFinished
	entry.Status = StatusAborted
	entry.ExitCode = -1
	entry.FailureReason = FailureReasonAgent
	entry.Error = "agent restarted while the run was in progress: " + verdict.Details
	entry.FinishedAt = time.Now()
	entry.LogLength = a.localLogSize(entry.ExecutionID)
	if err := a.journal.Save(entry); err != nil {
		log.Printf("journal aborted execution %s: %v", entry.ExecutionID, err)
		return
	}
	a.completeExecution(ctx, entry)
}

// blockExecution records that the agent cannot account for an execution and
// stops accepting work until an operator resolves it.
func (a *Agent) blockExecution(ctx context.Context, entry *journalEntry, details string) {
	entry.Phase = journalBlocked
	entry.Attention = details
	if err := a.journal.Save(entry); err != nil {
		log.Printf("journal blocked execution %s: %v", entry.ExecutionID, err)
	}
	if err := a.client.ReportAttention(ctx, entry.ExecutionID, details); err != nil {
		log.Printf("report attention for %s: %v", entry.ExecutionID, err)
	}
	a.markBlocked(entry.ExecutionID)
}
