package main

import (
	"net/http"
)

// queueEntry is one ordered queue position with the reason it is still waiting.
type queueEntry struct {
	Position       int               `json:"position"`
	Run            *Execution        `json:"run"`
	Reason         string            `json:"reason"`
	EligibleAgents []string          `json:"eligible_agents"`
	Parameters     map[string]string `json:"parameters,omitempty"`
}

func (s *ControllerAPI) handleQueue(w http.ResponseWriter, r *http.Request, who principal) {
	views := s.controller.AgentViews()
	queued := s.controller.store.QueuedExecutions()
	entries := make([]queueEntry, 0, len(queued))
	for index, execution := range queued {
		eligible := EligibleAgents(views, execution.Labels)
		ids := make([]string, 0, len(eligible))
		for _, view := range eligible {
			ids = append(ids, view.Definition.ID)
		}
		entries = append(entries, queueEntry{
			Position:       index + 1,
			Run:            execution,
			Reason:         queueReason(views, execution.Labels),
			EligibleAgents: ids,
			Parameters:     execution.Parameters,
		})
	}

	active := make([]*Execution, 0)
	for _, execution := range s.controller.store.Executions() {
		if isExecutionActive(execution.Status) {
			active = append(active, execution)
		}
	}
	respondJSON(w, map[string]any{
		"queue":  entries,
		"active": active,
		"agents": s.agentSummaries(),
	})
}

type queueCancelRequest struct {
	IDs []string `json:"ids"`
}

// handleQueueCancel cancels a selected batch and reports each outcome.
func (s *ControllerAPI) handleQueueCancel(w http.ResponseWriter, r *http.Request, who principal) {
	var request queueCancelRequest
	if err := decodeJSONBody(r, &request, 256<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid cancel request: "+err.Error())
		return
	}
	if len(request.IDs) == 0 {
		respondError(w, http.StatusBadRequest, "no execution ids were provided")
		return
	}
	if len(request.IDs) > maxRunPageSize {
		respondError(w, http.StatusBadRequest, "too many execution ids in one request")
		return
	}
	respondJSON(w, map[string]any{"ok": true, "results": s.controller.CancelMany(request.IDs)})
}
