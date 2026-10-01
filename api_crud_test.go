package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestJobCRUDThroughTheAPI(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	// Create.
	job := JobConfig{ID: "lint", Name: "Lint", Labels: []string{"linux"}, Script: "echo lint", Timeout: "5m"}
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs", job), http.StatusOK)
	if _, ok := findJob(controller.Config(), "lint"); !ok {
		t.Fatal("the job should exist after creation")
	}
	// Duplicate create is rejected.
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs", job), http.StatusBadRequest)

	// Read one and all.
	recorder := request(t, api, session, http.MethodGet, "/api/jobs/lint", nil)
	requireStatus(t, recorder, http.StatusOK)
	var single jobView
	decodeBody(t, recorder, &single)
	if single.ID != "lint" || !single.Enabled {
		t.Fatalf("unexpected job view %+v", single)
	}
	recorder = request(t, api, session, http.MethodGet, "/api/jobs", nil)
	requireStatus(t, recorder, http.StatusOK)
	var list struct{ Jobs []jobView }
	decodeBody(t, recorder, &list)
	if len(list.Jobs) != 3 {
		t.Fatalf("expected three jobs, got %d", len(list.Jobs))
	}

	// Update, including disabling.
	job.Script = "echo lint again"
	job.Enabled = boolPointer(false)
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/jobs/lint", job), http.StatusOK)
	updated, _ := findJob(controller.Config(), "lint")
	if updated.Script != "echo lint again" || updated.IsEnabled() {
		t.Fatalf("unexpected updated job %+v", updated)
	}
	// A disabled job cannot be queued.
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/lint/runs", nil), http.StatusConflict)

	// The id cannot be changed, and an invalid document is refused.
	renamed := job
	renamed.ID = "other"
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/jobs/lint", renamed), http.StatusBadRequest)
	broken := job
	broken.Script = ""
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/jobs/lint", broken), http.StatusBadRequest)
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/jobs/ghost", JobConfig{ID: "ghost", Script: "x"}), http.StatusNotFound)

	// Delete.
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/jobs/lint", nil), http.StatusOK)
	if _, ok := findJob(controller.Config(), "lint"); ok {
		t.Fatal("the job should be gone")
	}
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/jobs/lint", nil), http.StatusNotFound)
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/jobs/lint", nil), http.StatusNotFound)
}

// TestJobViewPresentsResolvedOptionsAndEligibleAgents backs the execute
// preview in the Web UI.
func TestJobViewPresentsResolvedOptionsAndEligibleAgents(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "mac-one")

	recorder := request(t, api, session, http.MethodGet, "/api/jobs/ios-build", nil)
	requireStatus(t, recorder, http.StatusOK)
	var view jobView
	decodeBody(t, recorder, &view)

	if len(view.Parameters) != 2 {
		t.Fatalf("expected two parameters, got %d", len(view.Parameters))
	}
	project := view.Parameters[0]
	if project.ID != "project" || len(project.Options) != 1 || project.Options[0].Value != "alpha" {
		t.Fatalf("the project parameter must resolve to label-filtered options, got %+v", project)
	}
	if len(view.EligibleAgents) != 1 || view.EligibleAgents[0].ID != "mac-one" {
		t.Fatalf("expected only the mac agent to be eligible, got %+v", view.EligibleAgents)
	}
	if !view.Runnable {
		t.Fatal("the job should be runnable while a matching agent is free")
	}

	// With the agent offline the preview says so without hiding the agent.
	androidView := request(t, api, session, http.MethodGet, "/api/jobs/android-build", nil)
	requireStatus(t, androidView, http.StatusOK)
	var android jobView
	decodeBody(t, androidView, &android)
	if len(android.EligibleAgents) != 2 {
		t.Fatalf("expected two eligible linux agents, got %d", len(android.EligibleAgents))
	}
	if android.Runnable {
		t.Fatal("no linux agent is online, so the job is not runnable right now")
	}
}

func TestCatalogCRUDThroughTheAPI(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	catalog := CatalogConfig{ID: "devices", Options: []OptionConfig{{Value: "pixel"}}}
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/catalogs", catalog), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/catalogs", catalog), http.StatusBadRequest)

	recorder := request(t, api, session, http.MethodGet, "/api/catalogs/projects", nil)
	requireStatus(t, recorder, http.StatusOK)
	var view catalogView
	decodeBody(t, recorder, &view)
	if len(view.UsedBy) != 2 {
		t.Fatalf("the projects catalog is read by two jobs, got %+v", view.UsedBy)
	}
	for _, usage := range view.UsedBy {
		if usage.Matches == 0 {
			t.Fatalf("usage should report how many options survive the label filter: %+v", usage)
		}
	}

	// Adding one project option updates both job lists at once.
	projects, _ := findCatalog(controller.Config(), "projects")
	projects.Options = append(projects.Options, OptionConfig{
		Value: "gamma", Labels: []string{"android", "ios"}, Values: map[string]string{"path": "gamma"},
	})
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/catalogs/projects", projects), http.StatusOK)
	for _, jobID := range []string{"android-build", "ios-build"} {
		recorder := request(t, api, session, http.MethodGet, "/api/jobs/"+jobID, nil)
		requireStatus(t, recorder, http.StatusOK)
		var job jobView
		decodeBody(t, recorder, &job)
		if !optionValueExists(job.Parameters[0].Options, "gamma") {
			t.Fatalf("job %s should offer the new project", jobID)
		}
	}

	// An id change and an invalid document are refused.
	renamed := projects
	renamed.ID = "other"
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/catalogs/projects", renamed), http.StatusBadRequest)
	empty := CatalogConfig{ID: "projects"}
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/catalogs/projects", empty), http.StatusBadRequest)

	// A catalog still used by a job cannot be deleted.
	recorder = request(t, api, session, http.MethodDelete, "/api/catalogs/projects", nil)
	requireStatus(t, recorder, http.StatusBadRequest)
	if !strings.Contains(recorder.Body.String(), "is used by job") {
		t.Fatalf("expected a usage explanation, got %s", recorder.Body.String())
	}
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/catalogs/devices", nil), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/catalogs/devices", nil), http.StatusNotFound)
}

func TestAgentCRUDThroughTheAPI(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	definition := AgentDefinition{ID: "mac-two", Labels: []string{"macos", "ios"}}
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/agents", definition), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/agents", definition), http.StatusBadRequest)

	recorder := request(t, api, session, http.MethodGet, "/api/agents", nil)
	requireStatus(t, recorder, http.StatusOK)
	var list struct{ Agents []agentSummary }
	decodeBody(t, recorder, &list)
	if len(list.Agents) != 4 {
		t.Fatalf("expected four agents, got %d", len(list.Agents))
	}
	for _, agent := range list.Agents {
		if agent.Enrolled {
			t.Fatalf("agent %s should not be enrolled yet", agent.ID)
		}
	}

	// Labels, enable, and pause are all centrally managed.
	definition.Labels = []string{"macos", "ios", "fast"}
	definition.Paused = true
	definition.Enabled = boolPointer(false)
	requireStatus(t, request(t, api, session, http.MethodPut, "/api/agents/mac-two", definition), http.StatusOK)
	stored, _ := findAgentDefinition(controller.Config(), "mac-two")
	if len(stored.Labels) != 3 || !stored.Paused || stored.IsEnabled() {
		t.Fatalf("unexpected stored agent %+v", stored)
	}

	recorder = request(t, api, session, http.MethodGet, "/api/agents/mac-two", nil)
	requireStatus(t, recorder, http.StatusOK)
	var summary agentSummary
	decodeBody(t, recorder, &summary)
	if !summary.Paused || summary.Enabled || summary.Online {
		t.Fatalf("unexpected summary %+v", summary)
	}

	requireStatus(t, request(t, api, session, http.MethodPut, "/api/agents/ghost", AgentDefinition{ID: "ghost"}), http.StatusNotFound)
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/agents/mac-two", nil), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/agents/mac-two", nil), http.StatusNotFound)
}

// TestDeletingAnActiveAgentIsRejected protects a live execution from losing
// its owner.
func TestDeletingAnActiveAgentIsRejected(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	recorder := request(t, api, session, http.MethodDelete, "/api/agents/linux-one", nil)
	requireStatus(t, recorder, http.StatusConflict)
	if !strings.Contains(recorder.Body.String(), "active execution") {
		t.Fatalf("expected an explanation, got %s", recorder.Body.String())
	}

	finishExecution(t, controller, execution.ID, StatusSuccess)
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/agents/linux-one", nil), http.StatusOK)
}

// TestConfigDocumentEditorValidatesBeforeReplacing covers the YAML editor.
func TestConfigDocumentEditorValidatesBeforeReplacing(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	recorder := request(t, api, session, http.MethodGet, "/api/config", nil)
	requireStatus(t, recorder, http.StatusOK)
	var document struct {
		Path    string
		Content string
	}
	decodeBody(t, recorder, &document)
	if !strings.Contains(document.Content, "android-build") || document.Path == "" {
		t.Fatalf("unexpected config document %+v", document)
	}

	requireStatus(t, request(t, api, session, http.MethodPost, "/api/config",
		map[string]string{"content": "role: controller\njobs:\n  - id: a\n"}), http.StatusBadRequest)
	if _, ok := findJob(controller.Config(), "android-build"); !ok {
		t.Fatal("a rejected document must not change the live config")
	}

	replacement := "role: controller\njobs:\n  - id: a\n    script: echo hi\n"
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/config",
		map[string]string{"content": replacement}), http.StatusOK)
	if _, ok := findJob(controller.Config(), "a"); !ok {
		t.Fatal("a valid document must be applied")
	}
	if _, ok := findJob(controller.Config(), "android-build"); ok {
		t.Fatal("the replacement document is authoritative")
	}
}

func TestAPITokenManagementEndpoints(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	requireStatus(t, request(t, api, session, http.MethodPost, "/api/tokens", map[string]string{"name": ""}), http.StatusBadRequest)
	recorder := request(t, api, session, http.MethodPost, "/api/tokens", map[string]string{"name": "ci"})
	requireStatus(t, recorder, http.StatusOK)
	var issued struct {
		ID    string
		Token string
	}
	decodeBody(t, recorder, &issued)
	if issued.ID == "" || issued.Token == "" {
		t.Fatalf("unexpected issue response %+v", issued)
	}

	recorder = request(t, api, session, http.MethodGet, "/api/tokens", nil)
	requireStatus(t, recorder, http.StatusOK)
	if !strings.Contains(recorder.Body.String(), issued.ID) {
		t.Fatal("the token list must include the new token id")
	}
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/tokens/"+issued.ID, nil), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/tokens/"+issued.ID, nil), http.StatusNotFound)
}
