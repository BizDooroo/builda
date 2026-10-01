package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"time"
)

// AssignmentFor returns the work packet currently assigned to an agent, if any.
func (c *Controller) AssignmentFor(agentID string) *AgentAssignment {
	var assignment *AgentAssignment
	c.store.read(func(st *controllerStateData) {
		for _, execution := range st.Executions {
			if execution == nil || execution.AgentID != agentID {
				continue
			}
			if execution.Status != StatusAssigned || execution.PermitRevoked || execution.CancelRequested {
				continue
			}
			assignment = &AgentAssignment{
				ExecutionID: execution.ID,
				JobID:       execution.JobID,
				JobName:     execution.JobName,
				Script:      execution.Script,
				TimeoutText: execution.TimeoutText,
				WorkdirPath: execution.WorkdirPath,
				Parameters:  cloneStringMap(execution.Parameters),
				Env:         cloneStringMap(execution.ParameterEnv),
				AssignedAt:  execution.AssignedAt,
			}
			return
		}
	})
	return assignment
}

// CancelRequestsFor lists the live executions of an agent whose cancellation
// the controller is waiting on.
func (c *Controller) CancelRequestsFor(agentID string) []string {
	var ids []string
	c.store.read(func(st *controllerStateData) {
		for _, execution := range st.Executions {
			if execution == nil || execution.AgentID != agentID {
				continue
			}
			if execution.CancelRequested && !isExecutionTerminal(execution.Status) {
				ids = append(ids, execution.ID)
			}
		}
	})
	sort.Strings(ids)
	return ids
}

// Reconcile aligns controller state with what an agent reports it knows after
// a restart or a network partition. The returned list names only executions
// this call acted on, so a standing escalation does not wake every poll. An assignment that never received a start
// permit provably never ran and is safely requeued. An execution whose permit
// was granted is uncertain and is flagged for an operator instead of being
// reassigned or retried.
func (c *Controller) Reconcile(agentID string, known []string) ([]string, error) {
	knownSet := make(map[string]bool, len(known))
	for _, id := range known {
		knownSet[id] = true
	}
	var unknown []string
	err := c.store.mutate(func(st *controllerStateData) error {
		changed := false
		for _, execution := range st.Executions {
			if execution == nil || execution.AgentID != agentID {
				continue
			}
			if isExecutionTerminal(execution.Status) || knownSet[execution.ID] {
				continue
			}
			if !execution.PermitGranted {
				unknown = append(unknown, execution.ID)
				execution.Status = StatusQueued
				execution.AgentID = ""
				execution.AssignedAt = time.Time{}
				execution.PermitRevoked = false
				changed = true
				continue
			}
			if !execution.NeedsAttention {
				execution.NeedsAttention = true
				execution.Attention = fmt.Sprintf("agent %s no longer tracks this execution after its start permit was granted; outcome is unknown", agentID)
				changed = true
				// Report it once, when it is first escalated. Repeating it on
				// every poll would make the agent re-poll immediately and spin.
				unknown = append(unknown, execution.ID)
			}
		}
		if !changed {
			return errNoStateChange
		}
		return nil
	})
	if err != nil && err != errNoStateChange {
		return nil, err
	}
	sort.Strings(unknown)
	return unknown, nil
}

// GrantPermit serializes the start grant against cancellation. A permit is
// only ever granted while the execution is still assigned, uncanceled, and
// owned by the requesting agent.
func (c *Controller) GrantPermit(agentID, executionID string) (AgentPermitResponse, error) {
	response := AgentPermitResponse{}
	err := c.store.mutate(func(st *controllerStateData) error {
		execution := st.find(executionID)
		if execution == nil {
			return errExecutionNotFound
		}
		if execution.AgentID != agentID {
			return errNotExecutionOwner
		}
		if execution.PermitRevoked || execution.CancelRequested {
			response = AgentPermitResponse{Granted: false, Reason: "cancellation was requested before the start permit"}
			return errNoStateChange
		}
		switch execution.Status {
		case StatusAssigned:
			execution.PermitGranted = true
			execution.Status = StatusRunning
			execution.StartedAt = time.Now()
			response = AgentPermitResponse{Granted: true}
			return nil
		case StatusRunning:
			// A duplicate permit request must never start a second process.
			// The agent journal is authoritative about whether it already ran.
			response = AgentPermitResponse{Granted: true, AlreadyGranted: true}
			return errNoStateChange
		default:
			response = AgentPermitResponse{Granted: false, Reason: "execution is " + execution.Status}
			return errNoStateChange
		}
	})
	if err != nil && err != errNoStateChange {
		return AgentPermitResponse{}, err
	}
	return response, nil
}

// Heartbeat records agent liveness and reports pending cancellation. It never
// stops a build: a lost connection only affects reporting.
func (c *Controller) Heartbeat(agentID string, request AgentHeartbeatRequest) AgentHeartbeatResponse {
	c.markAgentSeen(agentID, request.Version)
	response := AgentHeartbeatResponse{}
	if request.ExecutionID == "" {
		return response
	}
	c.store.read(func(st *controllerStateData) {
		execution := st.find(request.ExecutionID)
		if execution == nil || execution.AgentID != agentID {
			return
		}
		response.Known = true
		response.Status = execution.Status
		response.CancelRequested = execution.CancelRequested
		response.AckOffset = execution.LogOffset
	})
	if size, err := executionLogSize(c.Runtime().LogDir, request.ExecutionID); err == nil {
		response.AckOffset = size
	}
	return response
}

// AppendLog accepts an ordered log chunk at a byte offset. Duplicate chunks
// are idempotent, a chunk past the durable end is rejected with the current
// offset so the agent retransmits, and the file itself is the durable record.
func (c *Controller) AppendLog(agentID string, request AgentLogRequest) (int64, error) {
	if err := c.requireExecutionOwner(agentID, request.ExecutionID); err != nil {
		return 0, err
	}
	path := executionLogPath(c.Runtime().LogDir, request.ExecutionID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if request.Offset > size {
		// A gap would corrupt the log; ask the agent to resend from here.
		return size, nil
	}
	skip := size - request.Offset
	if skip >= int64(len(request.Data)) {
		return size, nil
	}
	if _, err := file.WriteAt(request.Data[skip:], size); err != nil {
		return size, err
	}
	if err := file.Sync(); err != nil {
		return size, err
	}
	acked := size + int64(len(request.Data)) - skip
	c.store.SetLogOffset(request.ExecutionID, acked)
	return acked, nil
}

// SubmitResult finalizes an execution. The result is only confirmed once the
// controller holds every log byte the agent produced.
func (c *Controller) SubmitResult(agentID string, request AgentResultRequest) (AgentResultResponse, error) {
	if err := c.requireExecutionOwner(agentID, request.ExecutionID); err != nil {
		return AgentResultResponse{}, err
	}
	size, err := executionLogSize(c.Runtime().LogDir, request.ExecutionID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AgentResultResponse{}, err
	}
	if size != request.LogLength {
		return AgentResultResponse{
			Accepted:  false,
			AckOffset: size,
			Reason:    fmt.Sprintf("controller holds %d of %d log bytes", size, request.LogLength),
		}, nil
	}
	status, err := normalizeResultStatus(request.Status)
	if err != nil {
		return AgentResultResponse{}, err
	}
	err = c.store.mutate(func(st *controllerStateData) error {
		execution := st.find(request.ExecutionID)
		if execution == nil {
			return errExecutionNotFound
		}
		if isExecutionTerminal(execution.Status) {
			// Duplicate result delivery is idempotent.
			return errNoStateChange
		}
		final := status
		if execution.CancelRequested && status != StatusSuccess {
			final = StatusCanceled
		}
		execution.Status = final
		execution.ExitCode = request.ExitCode
		execution.Error = request.Error
		execution.FailureReason = request.FailureReason
		execution.FinishedAt = time.Now()
		execution.LogOffset = size
		execution.LogComplete = true
		execution.NeedsAttention = false
		execution.Attention = ""
		if final == StatusCanceled && execution.CanceledAt.IsZero() {
			execution.CanceledAt = execution.FinishedAt
		}
		return nil
	})
	if err != nil && err != errNoStateChange {
		return AgentResultResponse{}, err
	}
	if scheduleErr := c.scheduleOnce(); scheduleErr != nil {
		log.Printf("schedule after result: %v", scheduleErr)
	}
	return AgentResultResponse{Accepted: true, AckOffset: size}, nil
}

// ReportAttention records that an agent cannot prove what happened to an
// execution. The controller blocks the agent rather than guessing.
func (c *Controller) ReportAttention(agentID string, request AgentAttentionRequest) error {
	return c.store.mutate(func(st *controllerStateData) error {
		execution := st.find(request.ExecutionID)
		if execution == nil {
			return errExecutionNotFound
		}
		if execution.AgentID != agentID {
			return errNotExecutionOwner
		}
		if isExecutionTerminal(execution.Status) {
			return errNoStateChange
		}
		execution.NeedsAttention = true
		execution.Attention = request.Message
		return nil
	})
}

func (c *Controller) requireExecutionOwner(agentID, executionID string) error {
	var err error
	c.store.read(func(st *controllerStateData) {
		execution := st.find(executionID)
		if execution == nil {
			err = errExecutionNotFound
			return
		}
		if execution.AgentID != agentID {
			err = errNotExecutionOwner
		}
	})
	return err
}

func normalizeResultStatus(status string) (string, error) {
	switch status {
	case StatusSuccess, StatusFailed, StatusCanceled, StatusAborted:
		return status, nil
	default:
		return "", fmt.Errorf("invalid result status %q", status)
	}
}
