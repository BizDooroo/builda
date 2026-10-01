package main

import (
	"time"
)

// Execution lifecycle states. QUEUED -> ASSIGNED -> RUNNING -> terminal.
// CANCELING is the transient state of a running execution whose cancellation
// has been requested but not yet confirmed by the owning agent.
const (
	StatusAssigned  = "ASSIGNED"
	StatusCanceling = "CANCELING"
)

// Queue reasons explain why a queued execution has not been assigned yet.
const (
	QueueReasonNoMatchingLabels = "no-matching-labels"
	QueueReasonOffline          = "offline"
	QueueReasonPaused           = "paused"
	QueueReasonBusy             = "busy"
	QueueReasonDisabled         = "disabled"
	QueueReasonReady            = "ready"
)

// Failure reasons recorded on terminal executions.
const (
	FailureReasonTimeout = "timeout"
	FailureReasonScript  = "script"
	FailureReasonAgent   = "agent"
)

// ExecutionOrigin records where a migrated execution came from so repeated
// imports stay idempotent.
type ExecutionOrigin struct {
	Machine     string            `json:"machine"`
	LegacyRunID string            `json:"legacy_run_id"`
	LegacyTask  string            `json:"legacy_task_id,omitempty"`
	TaskName    string            `json:"legacy_task_name,omitempty"`
	TimeoutText string            `json:"legacy_timeout,omitempty"`
	Script      string            `json:"legacy_script,omitempty"`
	ScriptHdr   string            `json:"legacy_script_header,omitempty"`
	Inputs      map[string]string `json:"legacy_inputs,omitempty"`
	ImportedAt  time.Time         `json:"imported_at"`
	LogImported bool              `json:"log_imported"`
	LogNote     string            `json:"log_note,omitempty"`
}

// Execution is one queued or completed job run. Everything the agent needs is
// snapshotted at enqueue and never rewritten by later config edits.
type Execution struct {
	ID              string                  `json:"id"`
	JobID           string                  `json:"job_id"`
	JobName         string                  `json:"job_name"`
	Labels          []string                `json:"labels,omitempty"`
	Script          string                  `json:"script"`
	TimeoutText     string                  `json:"timeout_text,omitempty"`
	WorkdirPath     string                  `json:"workdir_path,omitempty"`
	Parameters      map[string]string       `json:"parameters,omitempty"`
	ParameterEnv    map[string]string       `json:"parameter_env,omitempty"`
	SelectedOptions map[string]OptionConfig `json:"selected_options,omitempty"`
	JobSnapshot     JobConfig               `json:"job_snapshot"`

	Status          string `json:"status"`
	AgentID         string `json:"agent_id,omitempty"`
	RequestedBy     string `json:"requested_by,omitempty"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
	PermitGranted   bool   `json:"permit_granted,omitempty"`
	PermitRevoked   bool   `json:"permit_revoked,omitempty"`
	NeedsAttention  bool   `json:"needs_attention,omitempty"`
	Attention       string `json:"attention,omitempty"`

	LogOffset   int64 `json:"log_offset"`
	LogComplete bool  `json:"log_complete,omitempty"`

	ExitCode      int    `json:"exit_code"`
	Error         string `json:"error,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`

	RequestedAt time.Time `json:"requested_at"`
	AssignedAt  time.Time `json:"assigned_at,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	CanceledAt  time.Time `json:"canceled_at,omitempty"`

	Origin *ExecutionOrigin `json:"origin,omitempty"`
}

// AgentState is the persisted scheduling state of one agent. Liveness is
// deliberately not persisted: after a controller restart every agent is
// offline until it polls again.
type AgentState struct {
	ID             string    `json:"id"`
	LastAssignedAt time.Time `json:"last_assigned_at,omitempty"`
}

// controllerStateData is the single JSON snapshot persisted atomically.
type controllerStateData struct {
	Version    int                    `json:"version"`
	Sequence   uint64                 `json:"sequence"`
	Executions []*Execution           `json:"executions"`
	Agents     map[string]*AgentState `json:"agents"`
}

func (e *Execution) clone() *Execution {
	if e == nil {
		return nil
	}
	copied := *e
	copied.Labels = append([]string(nil), e.Labels...)
	copied.Parameters = cloneStringMap(e.Parameters)
	copied.ParameterEnv = cloneStringMap(e.ParameterEnv)
	copied.JobSnapshot = cloneJobConfig(e.JobSnapshot)
	if e.SelectedOptions != nil {
		options := make(map[string]OptionConfig, len(e.SelectedOptions))
		for key, option := range e.SelectedOptions {
			options[key] = cloneOption(option)
		}
		copied.SelectedOptions = options
	}
	if e.Origin != nil {
		origin := *e.Origin
		origin.Inputs = cloneStringMap(e.Origin.Inputs)
		copied.Origin = &origin
	}
	return &copied
}

func (s *AgentState) clone() *AgentState {
	if s == nil {
		return nil
	}
	copied := *s
	return &copied
}

func (d controllerStateData) clone() controllerStateData {
	next := controllerStateData{Version: d.Version, Sequence: d.Sequence}
	next.Executions = make([]*Execution, 0, len(d.Executions))
	for _, execution := range d.Executions {
		next.Executions = append(next.Executions, execution.clone())
	}
	next.Agents = make(map[string]*AgentState, len(d.Agents))
	for id, agent := range d.Agents {
		next.Agents[id] = agent.clone()
	}
	return next
}

func (d *controllerStateData) find(id string) *Execution {
	for _, execution := range d.Executions {
		if execution != nil && execution.ID == id {
			return execution
		}
	}
	return nil
}

func (d *controllerStateData) agent(id string) *AgentState {
	if d.Agents == nil {
		d.Agents = map[string]*AgentState{}
	}
	if existing, ok := d.Agents[id]; ok && existing != nil {
		return existing
	}
	state := &AgentState{ID: id}
	d.Agents[id] = state
	return state
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func cloneOption(option OptionConfig) OptionConfig {
	option.Labels = append([]string(nil), option.Labels...)
	option.Values = cloneStringMap(option.Values)
	return option
}

func cloneJobConfig(job JobConfig) JobConfig {
	job.Labels = append([]string(nil), job.Labels...)
	if job.Enabled != nil {
		job.Enabled = boolPointer(*job.Enabled)
	}
	params := make([]ParameterConfig, 0, len(job.Parameters))
	for _, param := range job.Parameters {
		param.CatalogLabels = append([]string(nil), param.CatalogLabels...)
		options := make([]OptionConfig, 0, len(param.Options))
		for _, option := range param.Options {
			options = append(options, cloneOption(option))
		}
		if len(options) > 0 {
			param.Options = options
		} else {
			param.Options = nil
		}
		params = append(params, param)
	}
	if len(params) > 0 {
		job.Parameters = params
	} else {
		job.Parameters = nil
	}
	return job
}

// isExecutionTerminal reports whether a status can no longer change.
func isExecutionTerminal(status string) bool {
	switch status {
	case StatusSuccess, StatusFailed, StatusCanceled, StatusAborted:
		return true
	default:
		return false
	}
}

// isExecutionActive reports whether an execution still occupies an agent slot.
func isExecutionActive(status string) bool {
	switch status {
	case StatusAssigned, StatusRunning, StatusCanceling:
		return true
	default:
		return false
	}
}
