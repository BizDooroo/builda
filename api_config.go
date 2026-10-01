package main

import (
	"io"
	"net/http"
	"os"
)

type saveConfigRequest struct {
	Content string `json:"content"`
}

// handleGetConfigDocument returns the raw controller YAML. The document never
// holds credentials: admin password verifiers and agent tokens live in the
// separate protected credential file.
func (s *ControllerAPI) handleGetConfigDocument(w http.ResponseWriter, r *http.Request, who principal) {
	path := s.controller.Runtime().ConfigPath
	data, err := os.ReadFile(path)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, map[string]any{"path": path, "content": string(data)})
}

// handleSaveConfigDocument validates a whole YAML document before replacing
// the active config through the same atomic write path as the CLI.
func (s *ControllerAPI) handleSaveConfigDocument(w http.ResponseWriter, r *http.Request, who principal) {
	var request saveConfigRequest
	if err := decodeJSONBody(r, &request, 2<<20); err != nil {
		data, readErr := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if readErr != nil || len(data) == 0 {
			respondError(w, http.StatusBadRequest, "invalid config request")
			return
		}
		request.Content = string(data)
	}
	cfg, err := parseControllerConfig([]byte(request.Content))
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.controller.editConfig(func(target *ControllerConfig) error {
		*target = cfg
		return nil
	}); err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}
