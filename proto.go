package main

import "time"

// Agent protocol. Agents connect outbound over authenticated HTTP long
// polling; the controller never dials an agent and agents never listen.
const (
	agentAPIPrefix = "/api/agent/v1/"

	agentStateIdle      = "idle"
	agentStateAccepted  = "accepted"
	agentStateRunning   = "running"
	agentStateUploading = "uploading"
	agentStateBlocked   = "blocked"
)

// AgentAssignment is the immutable work packet handed to an agent.
type AgentAssignment struct {
	ExecutionID string            `json:"execution_id"`
	JobID       string            `json:"job_id"`
	JobName     string            `json:"job_name"`
	Script      string            `json:"script"`
	TimeoutText string            `json:"timeout_text,omitempty"`
	WorkdirPath string            `json:"workdir_path,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	AssignedAt  time.Time         `json:"assigned_at"`
}

type AgentPollRequest struct {
	AgentID            string   `json:"agent_id"`
	Version            string   `json:"version,omitempty"`
	CurrentExecutionID string   `json:"current_execution_id,omitempty"`
	KnownExecutions    []string `json:"known_executions,omitempty"`
}

type AgentPollResponse struct {
	Assignment        *AgentAssignment `json:"assignment,omitempty"`
	CancelRequested   []string         `json:"cancel_requested,omitempty"`
	Unknown           []string         `json:"unknown,omitempty"`
	HeartbeatInterval string           `json:"heartbeat_interval"`
	PollTimeout       string           `json:"poll_timeout"`
}

type AgentPermitRequest struct {
	AgentID     string `json:"agent_id"`
	ExecutionID string `json:"execution_id"`
}

type AgentPermitResponse struct {
	Granted        bool   `json:"granted"`
	Reason         string `json:"reason,omitempty"`
	AlreadyGranted bool   `json:"already_granted,omitempty"`
}

type AgentHeartbeatRequest struct {
	AgentID     string `json:"agent_id"`
	Version     string `json:"version,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	State       string `json:"state,omitempty"`
}

type AgentHeartbeatResponse struct {
	CancelRequested bool   `json:"cancel_requested"`
	Known           bool   `json:"known"`
	Status          string `json:"status,omitempty"`
	AckOffset       int64  `json:"ack_offset"`
}

type AgentLogRequest struct {
	AgentID     string `json:"agent_id"`
	ExecutionID string `json:"execution_id"`
	Offset      int64  `json:"offset"`
	Data        []byte `json:"data"`
}

type AgentLogResponse struct {
	AckOffset int64 `json:"ack_offset"`
}

type AgentResultRequest struct {
	AgentID       string `json:"agent_id"`
	ExecutionID   string `json:"execution_id"`
	Status        string `json:"status"`
	ExitCode      int    `json:"exit_code"`
	Error         string `json:"error,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	LogLength     int64  `json:"log_length"`
}

type AgentResultResponse struct {
	Accepted  bool   `json:"accepted"`
	AckOffset int64  `json:"ack_offset"`
	Reason    string `json:"reason,omitempty"`
}

type AgentAttentionRequest struct {
	AgentID     string `json:"agent_id"`
	ExecutionID string `json:"execution_id"`
	Message     string `json:"message"`
}
