package main

import (
	"errors"
	"net/http"
	"strings"
)

type createTokenRequest struct {
	Name string `json:"name"`
}

// handleListTokens returns automation token metadata. Verifier hashes are
// never included in any API response.
func (s *ControllerAPI) handleListTokens(w http.ResponseWriter, r *http.Request, who principal) {
	respondJSON(w, map[string]any{
		"api_tokens":   s.controller.auth.APITokens(),
		"agent_tokens": s.controller.auth.AgentTokens(),
	})
}

// handleCreateToken issues an API bearer token for external automation and
// returns the raw secret exactly once.
func (s *ControllerAPI) handleCreateToken(w http.ResponseWriter, r *http.Request, who principal) {
	var request createTokenRequest
	if err := decodeJSONBody(r, &request, 8<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid token request: "+err.Error())
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "token name is required")
		return
	}
	secret, record, err := s.controller.auth.IssueAPIToken(name)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, map[string]any{
		"ok":    true,
		"id":    record.ID,
		"name":  record.Name,
		"token": secret,
		"note":  "store this token now; it is never shown again",
	})
}

func (s *ControllerAPI) handleRevokeToken(w http.ResponseWriter, r *http.Request, who principal) {
	if err := s.controller.auth.RevokeAPIToken(r.PathValue("id")); err != nil {
		if errors.Is(err, errTokenNotFound) {
			respondError(w, http.StatusNotFound, "token not found")
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}
