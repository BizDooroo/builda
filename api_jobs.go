package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// jobView adds resolved option lists and eligibility to a job definition so
// the Web UI can render an execute form without a second round trip.
type jobView struct {
	JobConfig
	Enabled        bool            `json:"enabled"`
	Parameters     []parameterView `json:"parameters"`
	EligibleAgents []agentSummary  `json:"eligible_agents"`
	Runnable       bool            `json:"runnable"`
}

type parameterView struct {
	ParameterConfig
	Options []OptionConfig `json:"options,omitempty"`
}

func (s *ControllerAPI) jobViews(cfg ControllerConfig) []jobView {
	views := s.controller.AgentViews()
	list := make([]jobView, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		list = append(list, buildJobView(cfg, job, views))
	}
	return list
}

func buildJobView(cfg ControllerConfig, job JobConfig, views []agentView) jobView {
	resolved := make([]parameterView, 0, len(job.Parameters))
	for _, param := range job.Parameters {
		resolved = append(resolved, parameterView{ParameterConfig: param, Options: parameterOptions(cfg, param)})
	}
	eligible := EligibleAgents(views, job.Labels)
	summaries := make([]agentSummary, 0, len(eligible))
	runnable := false
	for _, view := range eligible {
		summaries = append(summaries, newAgentSummary(view))
		if view.schedulable() {
			runnable = true
		}
	}
	view := jobView{JobConfig: job, Enabled: job.IsEnabled(), Parameters: resolved, EligibleAgents: summaries, Runnable: runnable}
	view.JobConfig.Parameters = nil
	return view
}

func (s *ControllerAPI) handleListJobs(w http.ResponseWriter, r *http.Request, who principal) {
	cfg := s.controller.Config()
	respondJSON(w, map[string]any{"jobs": s.jobViews(cfg)})
}

func (s *ControllerAPI) handleGetJob(w http.ResponseWriter, r *http.Request, who principal) {
	cfg := s.controller.Config()
	job, ok := findJob(cfg, r.PathValue("id"))
	if !ok {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	respondJSON(w, buildJobView(cfg, job, s.controller.AgentViews()))
}

func (s *ControllerAPI) handleCreateJob(w http.ResponseWriter, r *http.Request, who principal) {
	var job JobConfig
	if err := decodeJSONBody(r, &job, 512<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid job document: "+err.Error())
		return
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		if _, exists := findJob(*cfg, strings.TrimSpace(job.ID)); exists {
			return fmt.Errorf("job %q already exists", job.ID)
		}
		cfg.Jobs = append(cfg.Jobs, job)
		return nil
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "id": job.ID})
}

func (s *ControllerAPI) handleUpdateJob(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	var job JobConfig
	if err := decodeJSONBody(r, &job, 512<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid job document: "+err.Error())
		return
	}
	if strings.TrimSpace(job.ID) == "" {
		job.ID = id
	}
	if job.ID != id {
		respondError(w, http.StatusBadRequest, "job id cannot be changed")
		return
	}
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Jobs {
			if cfg.Jobs[index].ID == id {
				cfg.Jobs[index] = job
				return nil
			}
		}
		return errJobNotFound
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *ControllerAPI) handleDeleteJob(w http.ResponseWriter, r *http.Request, who principal) {
	id := r.PathValue("id")
	err := s.controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Jobs {
			if cfg.Jobs[index].ID == id {
				cfg.Jobs = append(cfg.Jobs[:index], cfg.Jobs[index+1:]...)
				return nil
			}
		}
		return errJobNotFound
	})
	if err != nil {
		respondConfigError(w, err)
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}

// handleRunJob enqueues one execution from declared parameters. Values may be
// supplied as query parameters or as a JSON object body.
func (s *ControllerAPI) handleRunJob(w http.ResponseWriter, r *http.Request, who principal) {
	values, wait, err := runRequestValues(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	execution, err := s.controller.Enqueue(r.PathValue("id"), values, who.User)
	if err != nil {
		switch {
		case errors.Is(err, errJobNotFound):
			respondError(w, http.StatusNotFound, "job not found")
		case errors.Is(err, errJobDisabled):
			respondError(w, http.StatusConflict, "job is disabled")
		default:
			respondError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if !wait {
		respondJSON(w, execution)
		return
	}
	s.respondAfterWait(w, r, execution.ID)
}

// respondAfterWait blocks until the execution finishes and returns the final
// record with its log. A client disconnect never cancels the execution.
func (s *ControllerAPI) respondAfterWait(w http.ResponseWriter, r *http.Request, id string) {
	if !s.controller.store.Wait(r.Context().Done(), id) {
		if r.Context().Err() != nil {
			respondError(w, http.StatusRequestTimeout, "client disconnected while waiting; the execution keeps running")
			return
		}
		respondError(w, http.StatusNotFound, "execution not found")
		return
	}
	execution, ok := s.controller.store.Find(id)
	if !ok {
		respondError(w, http.StatusNotFound, "execution not found")
		return
	}
	logText, _ := readExecutionLog(s.controller.Runtime().LogDir, id, 0, defaultLogLimit)
	respondJSON(w, map[string]any{"run": execution, "log": string(logText)})
}

// runRequestValues merges query parameters and an optional JSON body into the
// declared parameter values, isolating the reserved wait control parameter.
func runRequestValues(r *http.Request) (url.Values, bool, error) {
	values := url.Values{}
	for key, list := range r.URL.Query() {
		for _, value := range list {
			values.Add(key, value)
		}
	}
	if r.ContentLength > 0 {
		body := map[string]string{}
		if err := decodeJSONBody(r, &body, 256<<10); err != nil {
			return nil, false, fmt.Errorf("invalid parameter body: %w", err)
		}
		for key, value := range body {
			if values.Has(key) {
				return nil, false, fmt.Errorf("parameter %q was provided in both the query string and the body", key)
			}
			values.Set(key, value)
		}
	}
	wait, err := waitRequested(values)
	if err != nil {
		return nil, false, err
	}
	values.Del(runWaitParam)
	return values, wait, nil
}

func waitRequested(values url.Values) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(values.Get(runWaitParam))) {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be 1 or 0", runWaitParam)
	}
}

func respondConfigError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errConfigChanged):
		respondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errJobNotFound), errors.Is(err, errCatalogNotFound), errors.Is(err, errAgentNotFound):
		respondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errAgentBusy):
		respondError(w, http.StatusConflict, err.Error())
	default:
		respondError(w, http.StatusBadRequest, err.Error())
	}
}
