package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	errAgentNotFound = errors.New("agent not found")
	errAgentBusy     = errors.New("agent has an active execution")
)

// agentSummary is the UI-facing view of one agent. It never includes token
// material; only enrollment status is reported.
type agentSummary struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Description        string    `json:"description,omitempty"`
	Labels             []string  `json:"labels"`
	Enabled            bool      `json:"enabled"`
	Paused             bool      `json:"paused"`
	Online             bool      `json:"online"`
	Busy               bool      `json:"busy"`
	Blocked            bool      `json:"blocked"`
	BlockedReason      string    `json:"blocked_reason,omitempty"`
	CurrentExecutionID string    `json:"current_execution_id,omitempty"`
	CurrentStatus      string    `json:"current_status,omitempty"`
	LastSeen           time.Time `json:"last_seen,omitempty"`
	LastAssignedAt     time.Time `json:"last_assigned_at,omitempty"`
	Version            string    `json:"version,omitempty"`
	Enrolled           bool      `json:"enrolled"`
}

func newAgentSummary(view agentView) agentSummary {
	return agentSummary{
		ID:                 view.Definition.ID,
		Name:               view.Definition.Name,
		Description:        view.Definition.Description,
		Labels:             view.Definition.Labels,
		Enabled:            view.Definition.IsEnabled(),
		Paused:             view.Definition.Paused,
		Online:             view.Online,
		Busy:               view.Busy,
		Blocked:            view.Blocked,
		BlockedReason:      view.BlockedReason,
		CurrentExecutionID: view.CurrentExecutionID,
		CurrentStatus:      view.CurrentStatus,
		LastSeen:           view.LastSeen,
		LastAssignedAt:     view.LastAssignedAt,
		Version:            view.Version,
	}
}

func (s *ControllerAPI) agentSummaries() []agentSummary {
	views := s.controller.AgentViews()
	summaries := make([]agentSummary, 0, len(views))
	for _, view := range views {
		summary := newAgentSummary(view)
		summary.Enrolled = s.controller.auth.HasAgentToken(summary.ID)
		summaries = append(summaries, summary)
	}
	return summaries
}

func (s *ControllerAPI) handleListAgents(w http.ResponseWriter, r *http.Request, who principal) {
	respondJSON(w, map[string]any{"agents": s.agentSummaries()})
}

func (s *ControllerAPI) handleGetAgent(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	for _, summary := range s.agentSummaries() {
		if summary.ID == id {
			respondJSON(w, summary)
			return
		}
	}
	respondError(w, http.StatusNotFound, "agent not found")
}

func (s *ControllerAPI) handleCreateAgent(w http.ResponseWriter, r *http.Request, who principal) {
	var definition AgentDefinition
	if err := decodeJSONBody(r, &definition, 64<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid agent document: "+err.Error())
		return
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		if _, exists := findAgentDefinition(*cfg, strings.TrimSpace(definition.ID)); exists {
			return fmt.Errorf("agent %q already exists", definition.ID)
		}
		cfg.Agents = append(cfg.Agents, definition)
		return nil
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "id": definition.ID})
}

func (s *ControllerAPI) handleUpdateAgent(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	var definition AgentDefinition
	if err := decodeJSONBody(r, &definition, 64<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid agent document: "+err.Error())
		return
	}
	if strings.TrimSpace(definition.ID) == "" {
		definition.ID = id
	}
	if definition.ID != id {
		respondError(w, http.StatusBadRequest, "agent id cannot be changed")
		return
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Agents {
			if cfg.Agents[index].ID == id {
				cfg.Agents[index] = definition
				return nil
			}
		}
		return errAgentNotFound
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "id": id})
}

// handleDeleteAgent refuses to remove an agent that still owns work, so an
// active execution can never lose its owner.
func (s *ControllerAPI) handleDeleteAgent(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	for _, view := range s.controller.AgentViews() {
		if view.Definition.ID != id {
			continue
		}
		if view.Busy || view.Blocked {
			respondError(w, http.StatusConflict, "agent "+id+" has an active execution; cancel or resolve it first")
			return
		}
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Agents {
			if cfg.Agents[index].ID == id {
				cfg.Agents = append(cfg.Agents[:index], cfg.Agents[index+1:]...)
				return nil
			}
		}
		return errAgentNotFound
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	if err := s.controller.auth.RevokeAgentToken(id); err != nil && !errors.Is(err, errTokenNotFound) {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}

// handleRotateAgentToken issues a new enrollment token. The raw secret is
// returned exactly once and only its hash is persisted.
func (s *ControllerAPI) handleRotateAgentToken(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	cfg := s.controller.Config()
	if _, ok := findAgentDefinition(cfg, id); !ok {
		respondError(w, http.StatusNotFound, "agent not found")
		return
	}
	secret, record, err := s.controller.auth.IssueAgentToken(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, map[string]any{
		"ok":         true,
		"agent_id":   id,
		"token_id":   record.ID,
		"token":      secret,
		"created_at": record.CreatedAt,
		"note":       "store this token on the agent now; it is never shown again",
	})
}

func (s *ControllerAPI) handleRevokeAgentToken(w http.ResponseWriter, r *http.Request, who principal) {
	if err := s.controller.auth.RevokeAgentToken(r.PathValue("id")); err != nil {
		if errors.Is(err, errTokenNotFound) {
			respondError(w, http.StatusNotFound, "agent is not enrolled")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}
