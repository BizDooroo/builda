package main

import (
	"errors"
	"net/http"
	"time"
)

// handleAgentPoll is the single outbound connection point of an agent. The
// agent holds the request open until work, a cancellation, or the long poll
// deadline arrives. A dropped poll never stops a running build.
func (s *ControllerAPI) handleAgentPoll(w http.ResponseWriter, r *http.Request, who principal) {
	var request AgentPollRequest
	if err := decodeJSONBody(r, &request, 256<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid poll request")
		return
	}
	if !s.bindAgent(w, who, request.AgentID) {
		return
	}
	s.controller.markAgentSeen(who.AgentID, request.Version)
	unknown, err := s.controller.Reconcile(who.AgentID, request.KnownExecutions)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.controller.scheduleOnce(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	runtime := s.controller.Runtime()
	response := AgentPollResponse{
		Unknown:           unknown,
		HeartbeatInterval: runtime.HeartbeatInterval.String(),
		PollTimeout:       runtime.LongPollTimeout.String(),
	}
	deadline := time.NewTimer(runtime.LongPollTimeout)
	defer deadline.Stop()
	for {
		changed := s.controller.store.Subscribe()
		response.Assignment = s.controller.AssignmentFor(who.AgentID)
		response.CancelRequested = s.controller.CancelRequestsFor(who.AgentID)
		if response.Assignment != nil || len(response.CancelRequested) > 0 || len(response.Unknown) > 0 {
			respondJSON(w, response)
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			respondJSON(w, response)
			return
		case <-r.Context().Done():
			return
		case <-s.controller.stop:
			respondJSON(w, response)
			return
		}
	}
}

// handleAgentPermit grants the single start permission for an assignment.
func (s *ControllerAPI) handleAgentPermit(w http.ResponseWriter, r *http.Request, who principal) {
	var request AgentPermitRequest
	if err := decodeJSONBody(r, &request, 16<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid permit request")
		return
	}
	if !s.bindAgent(w, who, request.AgentID) {
		return
	}
	s.controller.markAgentSeen(who.AgentID, "")
	response, err := s.controller.GrantPermit(who.AgentID, request.ExecutionID)
	if err != nil {
		respondAgentError(w, err)
		return
	}
	respondJSON(w, response)
}

func (s *ControllerAPI) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request, who principal) {
	var request AgentHeartbeatRequest
	if err := decodeJSONBody(r, &request, 16<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid heartbeat request")
		return
	}
	if !s.bindAgent(w, who, request.AgentID) {
		return
	}
	respondJSON(w, s.controller.Heartbeat(who.AgentID, request))
}

// handleAgentLog accepts an ordered log chunk and acknowledges the durable
// byte offset the controller now holds.
func (s *ControllerAPI) handleAgentLog(w http.ResponseWriter, r *http.Request, who principal) {
	var request AgentLogRequest
	if err := decodeJSONBody(r, &request, 8<<20); err != nil {
		respondError(w, http.StatusBadRequest, "invalid log request")
		return
	}
	if !s.bindAgent(w, who, request.AgentID) {
		return
	}
	s.controller.markAgentSeen(who.AgentID, "")
	acked, err := s.controller.AppendLog(who.AgentID, request)
	if err != nil {
		respondAgentError(w, err)
		return
	}
	respondJSON(w, AgentLogResponse{AckOffset: acked})
}

func (s *ControllerAPI) handleAgentResult(w http.ResponseWriter, r *http.Request, who principal) {
	var request AgentResultRequest
	if err := decodeJSONBody(r, &request, 64<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid result request")
		return
	}
	if !s.bindAgent(w, who, request.AgentID) {
		return
	}
	s.controller.markAgentSeen(who.AgentID, "")
	response, err := s.controller.SubmitResult(who.AgentID, request)
	if err != nil {
		respondAgentError(w, err)
		return
	}
	respondJSON(w, response)
}

// handleAgentAttention records that an agent could not prove what happened to
// an execution. The controller blocks the agent instead of guessing.
func (s *ControllerAPI) handleAgentAttention(w http.ResponseWriter, r *http.Request, who principal) {
	var request AgentAttentionRequest
	if err := decodeJSONBody(r, &request, 16<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid attention request")
		return
	}
	if !s.bindAgent(w, who, request.AgentID) {
		return
	}
	s.controller.markAgentSeen(who.AgentID, "")
	if err := s.controller.ReportAttention(who.AgentID, request); err != nil && !errors.Is(err, errNoStateChange) {
		respondAgentError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}

// bindAgent enforces that a per-agent token may only act for its own identity
// and only while that agent is still configured.
func (s *ControllerAPI) bindAgent(w http.ResponseWriter, who principal, requestedID string) bool {
	if requestedID != "" && requestedID != who.AgentID {
		respondError(w, http.StatusForbidden, "agent token does not match the requested agent id")
		return false
	}
	if _, ok := findAgentDefinition(s.controller.Config(), who.AgentID); !ok {
		respondError(w, http.StatusForbidden, "agent is not configured on this controller")
		return false
	}
	return true
}

func respondAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errExecutionNotFound):
		respondError(w, http.StatusNotFound, "execution not found")
	case errors.Is(err, errNotExecutionOwner):
		respondError(w, http.StatusForbidden, "execution is not assigned to this agent")
	default:
		respondError(w, http.StatusInternalServerError, err.Error())
	}
}
