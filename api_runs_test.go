package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type runListResponse struct {
	Runs   []*Execution `json:"runs"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func listRuns(t *testing.T, api *ControllerAPI, session *Session, query string) runListResponse {
	t.Helper()
	recorder := request(t, api, session, http.MethodGet, "/api/runs"+query, nil)
	requireStatus(t, recorder, http.StatusOK)
	var response runListResponse
	decodeBody(t, recorder, &response)
	return response
}

func TestRunListFiltersByJobProjectAgentAndStatus(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one", "mac-one")

	android := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	ios := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
	finishExecution(t, controller, android.ID, StatusSuccess)
	beta := enqueue(t, controller, "android-build", map[string]string{"project": "beta"})
	finishExecution(t, controller, beta.ID, StatusFailed)

	if got := listRuns(t, api, session, "").Total; got != 3 {
		t.Fatalf("expected three runs, got %d", got)
	}
	if got := listRuns(t, api, session, "?job=android-build").Total; got != 2 {
		t.Fatalf("job filter returned %d runs", got)
	}
	if got := listRuns(t, api, session, "?project=beta").Total; got != 1 {
		t.Fatalf("project filter returned %d runs", got)
	}
	if got := listRuns(t, api, session, "?agent=mac-one").Total; got != 1 {
		t.Fatalf("agent filter returned %d runs", got)
	}
	if got := listRuns(t, api, session, "?status=SUCCESS").Total; got != 1 {
		t.Fatalf("status filter returned %d runs", got)
	}
	if got := listRuns(t, api, session, "?status=ACTIVE").Total; got != 1 {
		t.Fatalf("active filter returned %d runs", got)
	}
	if got := listRuns(t, api, session, "?status=TERMINAL").Total; got != 2 {
		t.Fatalf("terminal filter returned %d runs", got)
	}
	// Combined filters intersect.
	if got := listRuns(t, api, session, "?job=android-build&status=FAILED&project=beta").Total; got != 1 {
		t.Fatalf("combined filter returned %d runs", got)
	}
	// Filtering is read only.
	if statusOf(t, controller, ios.ID) != StatusAssigned {
		t.Fatal("listing runs must never change queue state")
	}
	// Invalid filters are rejected.
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/runs?status=BOGUS", nil), http.StatusBadRequest)
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/runs?limit=0", nil), http.StatusBadRequest)
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/runs?offset=-1", nil), http.StatusBadRequest)
}

func TestRunListPaginationIsBounded(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "mac-one")
	for i := 0; i < 5; i++ {
		execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
		finishExecution(t, controller, execution.ID, StatusSuccess)
	}

	page := listRuns(t, api, session, "?limit=2")
	if len(page.Runs) != 2 || page.Total != 5 || page.Limit != 2 {
		t.Fatalf("unexpected first page %+v", page)
	}
	second := listRuns(t, api, session, "?limit=2&offset=2")
	if len(second.Runs) != 2 || second.Offset != 2 {
		t.Fatalf("unexpected second page %+v", second)
	}
	if second.Runs[0].ID == page.Runs[0].ID {
		t.Fatal("pages must not repeat entries")
	}
	last := listRuns(t, api, session, "?limit=2&offset=4")
	if len(last.Runs) != 1 {
		t.Fatalf("unexpected last page size %d", len(last.Runs))
	}
	beyond := listRuns(t, api, session, "?offset=100")
	if len(beyond.Runs) != 0 {
		t.Fatal("an offset past the end must return an empty page")
	}
	// An oversized limit is clamped instead of rejected.
	clamped := listRuns(t, api, session, "?limit="+strconv.Itoa(maxRunPageSize*10))
	if clamped.Limit != maxRunPageSize {
		t.Fatalf("expected the limit to be clamped to %d, got %d", maxRunPageSize, clamped.Limit)
	}
}

func TestRunDetailLogCancelAndDelete(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	requireStatus(t, request(t, api, session, http.MethodGet, "/api/runs/"+execution.ID, nil), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/runs/missing", nil), http.StatusNotFound)

	// A run with no log yet returns an empty body rather than an error.
	recorder := request(t, api, session, http.MethodGet, "/api/runs/"+execution.ID+"/log", nil)
	requireStatus(t, recorder, http.StatusOK)
	if recorder.Body.Len() != 0 {
		t.Fatalf("expected an empty log, got %q", recorder.Body.String())
	}

	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	body := []byte("first line\nsecond line\n")
	if _, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: execution.ID, Data: body}); err != nil {
		t.Fatal(err)
	}
	recorder = request(t, api, session, http.MethodGet, "/api/runs/"+execution.ID+"/log", nil)
	requireStatus(t, recorder, http.StatusOK)
	if recorder.Body.String() != string(body) {
		t.Fatalf("unexpected log %q", recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Builda-Log-Offset"); got != strconv.Itoa(len(body)) {
		t.Fatalf("expected the next offset header, got %q", got)
	}
	// A follow request only receives the new tail.
	recorder = request(t, api, session, http.MethodGet, "/api/runs/"+execution.ID+"/log?offset=11", nil)
	requireStatus(t, recorder, http.StatusOK)
	if recorder.Body.String() != "second line\n" {
		t.Fatalf("unexpected tail %q", recorder.Body.String())
	}
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/runs/"+execution.ID+"/log?offset=-1", nil), http.StatusBadRequest)

	// Cancel through the API, then finish and delete.
	recorder = request(t, api, session, http.MethodPost, "/api/runs/"+execution.ID+"/cancel", nil)
	requireStatus(t, recorder, http.StatusOK)
	if statusOf(t, controller, execution.ID) != StatusCanceling {
		t.Fatal("expected the run to be canceling")
	}
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/runs/"+execution.ID, nil), http.StatusConflict)

	if _, err := controller.SubmitResult("linux-one", AgentResultRequest{
		ExecutionID: execution.ID, Status: StatusCanceled, LogLength: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/runs/"+execution.ID, nil), http.StatusOK)
	requireStatus(t, request(t, api, session, http.MethodDelete, "/api/runs/"+execution.ID, nil), http.StatusNotFound)
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/runs/missing/cancel", nil), http.StatusNotFound)
}

// TestRunJobWaitReturnsRunAndLog covers wait=1 and proves a client disconnect
// does not cancel the execution.
func TestRunJobWaitReturnsRunAndLog(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one")

	done := make(chan *testResponse, 1)
	go func() {
		recorder := request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs?project=alpha&wait=1", nil)
		done <- &testResponse{Code: recorder.Code, Body: recorder.Body.String()}
	}()

	var id string
	waitFor(t, "the waiting run to be assigned", func() bool {
		for _, execution := range controller.store.Executions() {
			if execution.Status == StatusAssigned {
				id = execution.ID
				return true
			}
		}
		return false
	})
	if _, err := controller.GrantPermit("linux-one", id); err != nil {
		t.Fatal(err)
	}
	logBody := []byte("building\n")
	if _, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: id, Data: logBody}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.SubmitResult("linux-one", AgentResultRequest{
		ExecutionID: id, Status: StatusSuccess, LogLength: int64(len(logBody)),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case response := <-done:
		if response.Code != http.StatusOK {
			t.Fatalf("wait returned %d: %s", response.Code, response.Body)
		}
		if !strings.Contains(response.Body, "building") {
			t.Fatalf("wait must return the log, got %s", response.Body)
		}
		if !strings.Contains(response.Body, StatusSuccess) {
			t.Fatalf("wait must return the final run, got %s", response.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}

	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs?project=alpha&wait=maybe", nil), http.StatusBadRequest)
}

type testResponse struct {
	Code int
	Body string
}

// TestWaitDisconnectKeepsTheRun proves abandoning the wait does not cancel.
func TestWaitDisconnectKeepsTheRun(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one")

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}

	recorder := requestWithCanceledContext(t, api, session, http.MethodGet, "/api/runs/"+execution.ID+"?wait=1")
	if recorder.Code != http.StatusRequestTimeout {
		t.Fatalf("expected a timeout status for a disconnected waiter, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if statusOf(t, controller, execution.ID) != StatusRunning {
		t.Fatal("a disconnected waiter must never cancel the run")
	}
}

func TestRunRerunUsesCurrentConfiguration(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one")
	first := enqueue(t, controller, "android-build", map[string]string{"project": "beta", "action": "release"})
	finishExecution(t, controller, first.ID, StatusSuccess)

	recorder := request(t, api, session, http.MethodPost, "/api/runs/"+first.ID+"/rerun", nil)
	requireStatus(t, recorder, http.StatusOK)
	var payload struct{ Run *Execution }
	decodeBody(t, recorder, &payload)
	if payload.Run.ID == first.ID {
		t.Fatal("a rerun must create a new execution")
	}
	if payload.Run.Parameters["project"] != "beta" || payload.Run.Parameters["action"] != "release" {
		t.Fatalf("a rerun must reuse the same parameters, got %v", payload.Run.Parameters)
	}

	// Removing the option makes the rerun fail loudly instead of silently
	// substituting a default.
	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Catalogs[0].Options {
			if cfg.Catalogs[0].Options[index].Value == "beta" {
				cfg.Catalogs[0].Options = append(cfg.Catalogs[0].Options[:index], cfg.Catalogs[0].Options[index+1:]...)
				break
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recorder = request(t, api, session, http.MethodPost, "/api/runs/"+first.ID+"/rerun", nil)
	requireStatus(t, recorder, http.StatusBadRequest)
	if !strings.Contains(recorder.Body.String(), "must be one of") {
		t.Fatalf("expected an invalid-option error, got %s", recorder.Body.String())
	}

	// Removing the parameter is reported too.
	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		for index := range cfg.Jobs {
			if cfg.Jobs[index].ID == "android-build" {
				cfg.Jobs[index].Parameters = cfg.Jobs[index].Parameters[:1]
				cfg.Jobs[index].WorkdirParam = "project"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recorder = request(t, api, session, http.MethodPost, "/api/runs/"+first.ID+"/rerun", nil)
	requireStatus(t, recorder, http.StatusBadRequest)
	if !strings.Contains(recorder.Body.String(), "no longer declares parameter") {
		t.Fatalf("expected a removed-parameter error, got %s", recorder.Body.String())
	}
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/runs/missing/rerun", nil), http.StatusNotFound)
}

func TestRunJobRejectsBadParameters(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/ghost/runs", nil), http.StatusNotFound)
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs", nil), http.StatusBadRequest)
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs?project=ghost", nil), http.StatusBadRequest)
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs?project=alpha&nope=1", nil), http.StatusBadRequest)

	// A JSON body and the query string cannot both set one parameter.
	recorder := request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs?project=alpha", map[string]string{"project": "beta"})
	requireStatus(t, recorder, http.StatusBadRequest)
	if !strings.Contains(recorder.Body.String(), "both the query string and the body") {
		t.Fatalf("expected a conflict explanation, got %s", recorder.Body.String())
	}

	// A body-only request works.
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/jobs/android-build/runs", map[string]string{"project": "alpha"}), http.StatusOK)
}

func TestQueueEndpointReportsOrderReasonsAndParameters(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	first := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	second := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	recorder := request(t, api, session, http.MethodGet, "/api/queue", nil)
	requireStatus(t, recorder, http.StatusOK)
	var payload struct {
		Queue  []queueEntry
		Active []*Execution
		Agents []agentSummary
	}
	decodeBody(t, recorder, &payload)
	if len(payload.Queue) != 2 {
		t.Fatalf("expected two queued entries, got %d", len(payload.Queue))
	}
	if payload.Queue[0].Run.ID != first.ID || payload.Queue[0].Position != 1 {
		t.Fatalf("the queue must be ordered by enqueue time, got %+v", payload.Queue[0])
	}
	if payload.Queue[1].Run.ID != second.ID || payload.Queue[1].Position != 2 {
		t.Fatalf("unexpected second entry %+v", payload.Queue[1])
	}
	if payload.Queue[0].Reason != QueueReasonOffline {
		t.Fatalf("expected the offline reason, got %q", payload.Queue[0].Reason)
	}
	if payload.Queue[0].Parameters["project"] != "alpha" {
		t.Fatalf("the queue must expose parameters, got %v", payload.Queue[0].Parameters)
	}
	if len(payload.Queue[0].EligibleAgents) != 2 {
		t.Fatalf("expected both linux agents to be listed, got %v", payload.Queue[0].EligibleAgents)
	}
	if len(payload.Agents) != 3 {
		t.Fatalf("the queue view must include the fleet, got %d agents", len(payload.Agents))
	}

	// Batch cancel.
	recorder = request(t, api, session, http.MethodPost, "/api/queue/cancel", queueCancelRequest{IDs: []string{first.ID, second.ID}})
	requireStatus(t, recorder, http.StatusOK)
	for _, id := range []string{first.ID, second.ID} {
		if statusOf(t, controller, id) != StatusCanceled {
			t.Fatalf("%s should be canceled", id)
		}
	}
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/queue/cancel", queueCancelRequest{}), http.StatusBadRequest)
}
