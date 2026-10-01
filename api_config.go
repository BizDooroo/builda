package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

// maxConfigDocumentBytes bounds a config document submitted over HTTP.
const maxConfigDocumentBytes = 2 << 20

type saveConfigRequest struct {
	Content string `json:"content"`
}

// configDocumentFromBody accepts either a JSON envelope carrying the document
// or the raw YAML document itself. The envelope is detected by its shape
// rather than by whether parsing happened to succeed, so a YAML fragment that
// is also valid JSON cannot be mistaken for an envelope with no content.
func configDocumentFromBody(body []byte) (string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err == nil {
		if raw, ok := envelope["content"]; ok {
			var content string
			if err := json.Unmarshal(raw, &content); err != nil {
				return "", errors.New("config document content must be a string")
			}
			if strings.TrimSpace(content) == "" {
				return "", errors.New("config document is empty")
			}
			return content, nil
		}
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", errors.New("config document is empty")
	}
	return string(body), nil
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
	// Read the body once. Decoding first and then reading the rest would hand
	// the YAML fallback only the tail the JSON decoder had not buffered, which
	// would install a silently truncated document.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigDocumentBytes+1))
	if err != nil {
		respondError(w, http.StatusBadRequest, "config document could not be read")
		return
	}
	if len(body) > maxConfigDocumentBytes {
		respondError(w, http.StatusRequestEntityTooLarge, "config document is too large")
		return
	}
	if len(body) == 0 {
		respondError(w, http.StatusBadRequest, "config document is empty")
		return
	}
	content, err := configDocumentFromBody(body)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := parseControllerConfig([]byte(content))
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
