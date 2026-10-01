package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"
)

var (
	errJobNotFound       = errors.New("job not found")
	errJobDisabled       = errors.New("job is disabled")
	errExecutionDone     = errors.New("execution already finished")
	errNotExecutionOwner = errors.New("execution is not assigned to this agent")
)

// Enqueue validates parameters against the current job definition and appends
// a new execution with an immutable snapshot of everything needed to run it.
func (c *Controller) Enqueue(jobID string, values url.Values, requestedBy string) (*Execution, error) {
	cfg := c.Config()
	job, ok := findJob(cfg, jobID)
	if !ok {
		return nil, errJobNotFound
	}
	if !job.IsEnabled() {
		return nil, errJobDisabled
	}
	resolved, err := resolveParameters(cfg, job, values)
	if err != nil {
		return nil, err
	}
	execution := &Execution{
		ID:              newExecutionID(),
		JobID:           job.ID,
		JobName:         job.Name,
		Labels:          append([]string(nil), job.Labels...),
		Script:          job.Script,
		TimeoutText:     job.Timeout,
		WorkdirPath:     resolved.WorkdirPath,
		Parameters:      resolved.Values,
		ParameterEnv:    resolved.Env,
		SelectedOptions: resolved.Options,
		JobSnapshot:     cloneJobConfig(job),
		Status:          StatusQueued,
		RequestedBy:     requestedBy,
		ExitCode:        -1,
		RequestedAt:     time.Now(),
	}
	if err := c.store.mutate(func(st *controllerStateData) error {
		st.Executions = append(st.Executions, execution.clone())
		return nil
	}); err != nil {
		return nil, err
	}
	if err := c.scheduleOnce(); err != nil {
		log.Printf("schedule after enqueue: %v", err)
	}
	stored, _ := c.store.Find(execution.ID)
	return stored, nil
}

// Cancel moves an execution toward CANCELED. A queued execution is canceled
// immediately. An assigned execution that never obtained a start permit has
// its permit revoked, which proves the script never ran. A running execution
// is only marked CANCELED once the owning agent confirms the process group
// ended, so an offline agent leaves a durable pending cancellation instead of
// a fabricated completion.
func (c *Controller) Cancel(id string) (*Execution, error) {
	err := c.store.mutate(func(st *controllerStateData) error {
		execution := st.find(id)
		if execution == nil {
			return errExecutionNotFound
		}
		now := time.Now()
		switch execution.Status {
		case StatusQueued:
			execution.Status = StatusCanceled
			execution.CancelRequested = true
			execution.CanceledAt = now
			execution.FinishedAt = now
			execution.LogComplete = true
			execution.Error = "canceled before assignment"
			return nil
		case StatusAssigned:
			execution.CancelRequested = true
			if execution.CanceledAt.IsZero() {
				execution.CanceledAt = now
			}
			if !execution.PermitGranted {
				execution.PermitRevoked = true
				execution.Status = StatusCanceled
				execution.FinishedAt = now
				execution.LogComplete = true
				execution.Error = "canceled before the agent was granted permission to start"
				return nil
			}
			execution.Status = StatusCanceling
			return nil
		case StatusRunning, StatusCanceling:
			execution.CancelRequested = true
			if execution.CanceledAt.IsZero() {
				execution.CanceledAt = now
			}
			execution.Status = StatusCanceling
			return nil
		default:
			return errExecutionDone
		}
	})
	if err != nil {
		return nil, err
	}
	if scheduleErr := c.scheduleOnce(); scheduleErr != nil {
		log.Printf("schedule after cancel: %v", scheduleErr)
	}
	execution, _ := c.store.Find(id)
	return execution, nil
}

// CancelMany cancels a batch and reports per-execution outcomes.
func (c *Controller) CancelMany(ids []string) map[string]string {
	results := make(map[string]string, len(ids))
	for _, id := range ids {
		execution, err := c.Cancel(id)
		switch {
		case err == nil:
			results[id] = execution.Status
		case errors.Is(err, errExecutionNotFound):
			results[id] = "not-found"
		case errors.Is(err, errExecutionDone):
			results[id] = "already-finished"
		default:
			results[id] = "error: " + err.Error()
		}
	}
	return results
}

// Rerun queues a new execution from a historical one using the current job
// configuration. Parameters that no longer exist or no longer resolve to a
// valid option are rejected instead of silently dropped.
func (c *Controller) Rerun(id, requestedBy string) (*Execution, error) {
	previous, ok := c.store.Find(id)
	if !ok {
		return nil, errExecutionNotFound
	}
	cfg := c.Config()
	job, ok := findJob(cfg, previous.JobID)
	if !ok {
		return nil, fmt.Errorf("job %q no longer exists", previous.JobID)
	}
	declared := map[string]bool{}
	for _, param := range job.Parameters {
		declared[param.ID] = true
	}
	for name, value := range previous.Parameters {
		if value == "" {
			continue
		}
		if !declared[name] {
			return nil, fmt.Errorf("job %q no longer declares parameter %q", job.ID, name)
		}
	}
	return c.Enqueue(job.ID, parameterValuesToQuery(previous.Parameters), requestedBy)
}

// ResolveAttention closes out an execution an agent could not prove ended.
// The operator decides the outcome; the controller never guesses.
func (c *Controller) ResolveAttention(id, note string) (*Execution, error) {
	err := c.store.mutate(func(st *controllerStateData) error {
		execution := st.find(id)
		if execution == nil {
			return errExecutionNotFound
		}
		if !execution.NeedsAttention {
			return errors.New("execution is not blocked on operator attention")
		}
		now := time.Now()
		execution.NeedsAttention = false
		execution.Attention = ""
		if !isExecutionTerminal(execution.Status) {
			execution.Status = StatusAborted
			execution.FinishedAt = now
			execution.LogComplete = true
			execution.FailureReason = FailureReasonAgent
			if note == "" {
				note = "operator resolved an unverifiable agent execution"
			}
			execution.Error = note
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if scheduleErr := c.scheduleOnce(); scheduleErr != nil {
		log.Printf("schedule after attention resolve: %v", scheduleErr)
	}
	execution, _ := c.store.Find(id)
	return execution, nil
}

// schedulerLoop reconciles the queue on every state change and on a slow tick
// so an agent that comes back online is picked up without a new mutation.
func (c *Controller) schedulerLoop(interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		changed := c.store.Subscribe()
		select {
		case <-c.stop:
			return
		case <-ticker.C:
		case <-changed:
		}
		if err := c.scheduleOnce(); err != nil {
			log.Printf("schedule: %v", err)
		}
	}
}
