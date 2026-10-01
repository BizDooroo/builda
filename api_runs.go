package main

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Bounded history and log responses keep the UI responsive on a long history.
const (
	defaultRunPageSize = 100
	maxRunPageSize     = 1000
	defaultLogLimit    = 2 << 20
	maxLogLimit        = 8 << 20
)

// runFilter is the read-only history filter. It never changes queue state.
type runFilter struct {
	JobID   string
	Project string
	AgentID string
	Status  string
	Limit   int
	Offset  int
}

func parseRunFilter(r *http.Request) (runFilter, error) {
	query := r.URL.Query()
	filter := runFilter{
		JobID:   strings.TrimSpace(query.Get("job")),
		Project: strings.TrimSpace(query.Get("project")),
		AgentID: strings.TrimSpace(query.Get("agent")),
		Status:  strings.ToUpper(strings.TrimSpace(query.Get("status"))),
		Limit:   defaultRunPageSize,
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return runFilter{}, errors.New("limit must be a positive integer")
		}
		if limit > maxRunPageSize {
			limit = maxRunPageSize
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return runFilter{}, errors.New("offset must be zero or greater")
		}
		filter.Offset = offset
	}
	switch filter.Status {
	case "", StatusQueued, StatusAssigned, StatusRunning, StatusCanceling,
		StatusSuccess, StatusFailed, StatusCanceled, StatusAborted, "ACTIVE", "TERMINAL":
	default:
		return runFilter{}, errors.New("unknown status filter " + filter.Status)
	}
	return filter, nil
}

func (f runFilter) matches(execution *Execution) bool {
	if f.JobID != "" && execution.JobID != f.JobID {
		return false
	}
	if f.AgentID != "" && execution.AgentID != f.AgentID {
		return false
	}
	if f.Project != "" && execution.Parameters["project"] != f.Project {
		return false
	}
	switch f.Status {
	case "":
		return true
	case "ACTIVE":
		return !isExecutionTerminal(execution.Status)
	case "TERMINAL":
		return isExecutionTerminal(execution.Status)
	default:
		return execution.Status == f.Status
	}
}

func (s *ControllerAPI) handleListRuns(w http.ResponseWriter, r *http.Request, who principal) {
	filter, err := parseRunFilter(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	all := s.controller.store.Executions()
	matched := make([]*Execution, 0, len(all))
	for _, execution := range all {
		if filter.matches(execution) {
			matched = append(matched, execution)
		}
	}
	total := len(matched)
	start := filter.Offset
	if start > total {
		start = total
	}
	end := start + filter.Limit
	if end > total {
		end = total
	}
	respondJSON(w, map[string]any{
		"runs":   matched[start:end],
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func (s *ControllerAPI) handleGetRun(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	execution, ok := s.controller.store.Find(id)
	if !ok {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	wait, err := waitRequested(r.URL.Query())
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if wait {
		s.respondAfterWait(w, r, id)
		return
	}
	respondJSON(w, execution)
}

// handleRunLog returns a bounded slice of the durable execution log.
func (s *ControllerAPI) handleRunLog(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	if _, ok := s.controller.store.Find(id); !ok {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	offset, limit, err := logRange(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, next, err := readExecutionLogRange(s.controller.Runtime().LogDir, id, offset, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Builda-Log-Offset", strconv.FormatInt(next, 10))
	_, _ = w.Write(data)
}

func logRange(r *http.Request) (int64, int64, error) {
	query := r.URL.Query()
	offset := int64(0)
	limit := int64(defaultLogLimit)
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return 0, 0, errors.New("offset must be zero or greater")
		}
		offset = parsed
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, 0, errors.New("limit must be a positive integer")
		}
		if parsed > maxLogLimit {
			parsed = maxLogLimit
		}
		limit = parsed
	}
	return offset, limit, nil
}

func (s *ControllerAPI) handleCancelRun(w http.ResponseWriter, r *http.Request, who principal) {
	execution, err := s.controller.Cancel(r.PathValue("id"))
	if err != nil {
		respondCancelError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "run": execution})
}

func (s *ControllerAPI) handleRerunRun(w http.ResponseWriter, r *http.Request, who principal) {
	execution, err := s.controller.Rerun(r.PathValue("id"), who.User)
	if err != nil {
		if errors.Is(err, errExecutionNotFound) {
			respondError(w, http.StatusNotFound, "run not found")
			return
		}
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, map[string]any{"ok": true, "run": execution})
}

func (s *ControllerAPI) handleResolveRun(w http.ResponseWriter, r *http.Request, who principal) {
	note := strings.TrimSpace(r.URL.Query().Get("note"))
	execution, err := s.controller.ResolveAttention(r.PathValue("id"), note)
	if err != nil {
		if errors.Is(err, errExecutionNotFound) {
			respondError(w, http.StatusNotFound, "run not found")
			return
		}
		respondError(w, http.StatusConflict, err.Error())
		return
	}
	respondJSON(w, map[string]any{"ok": true, "run": execution})
}

func (s *ControllerAPI) handleDeleteRun(w http.ResponseWriter, r *http.Request, who principal) {
	err := s.controller.store.DeleteExecution(r.PathValue("id"))
	switch {
	case err == nil:
		respondJSON(w, map[string]any{"ok": true})
	case errors.Is(err, errExecutionNotFound):
		respondError(w, http.StatusNotFound, "run not found")
	case errors.Is(err, errExecutionActive):
		respondError(w, http.StatusConflict, "cannot delete a run that has not finished")
	default:
		respondError(w, http.StatusInternalServerError, err.Error())
	}
}

func respondCancelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errExecutionNotFound):
		respondError(w, http.StatusNotFound, "run not found")
	case errors.Is(err, errExecutionDone):
		respondError(w, http.StatusConflict, "run already finished")
	default:
		respondError(w, http.StatusInternalServerError, err.Error())
	}
}

// readExecutionLogRange reads a bounded window of an execution log and returns
// the offset the caller should request next.
func readExecutionLogRange(logDir, id string, offset, limit int64) ([]byte, int64, error) {
	path := executionLogPath(logDir, id)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if offset >= info.Size() {
		return nil, info.Size(), nil
	}
	size := info.Size() - offset
	if size > limit {
		size = limit
	}
	buf := make([]byte, size)
	read, err := file.ReadAt(buf, offset)
	if err != nil && read == 0 {
		return nil, offset, err
	}
	return buf[:read], offset + int64(read), nil
}

func readExecutionLog(logDir, id string, offset, limit int64) ([]byte, error) {
	data, _, err := readExecutionLogRange(logDir, id, offset, limit)
	return data, err
}
