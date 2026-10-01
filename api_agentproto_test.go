package main

import (
	"net/http"
	"os"
	"testing"
	"time"
)

// agentCredential enrols an agent and returns its bearer token.
func agentCredential(t *testing.T, controller *Controller, agentID string) string {
	t.Helper()
	secret, _, err := controller.auth.IssueAgentToken(agentID)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestAgentPollDeliversAssignmentWithFullPayload(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	token := agentCredential(t, controller, "linux-one")
	enqueue(t, controller, "android-build", map[string]string{"project": "beta", "action": "release"})

	recorder := tokenRequest(t, api, token, http.MethodPost, "/api/agent/v1/poll", AgentPollRequest{AgentID: "linux-one"})
	requireStatus(t, recorder, http.StatusOK)
	var response AgentPollResponse
	decodeBody(t, recorder, &response)
	if response.Assignment == nil {
		t.Fatalf("expected an assignment, got %s", recorder.Body.String())
	}
	assignment := response.Assignment
	if assignment.Script != "echo android" || assignment.TimeoutText != "45m" {
		t.Fatalf("unexpected assignment %+v", assignment)
	}
	if assignment.WorkdirPath != "nested/beta" {
		t.Fatalf("the assignment must carry the resolved project path, got %q", assignment.WorkdirPath)
	}
	if assignment.Env["BUILDA_PARAM_PROJECT"] != "beta" || assignment.Env["BUILDA_PARAM_ACTION"] != "release" {
		t.Fatalf("unexpected env %v", assignment.Env)
	}
	if response.HeartbeatInterval == "" || response.PollTimeout == "" {
		t.Fatal("the poll response must advertise the timings")
	}
}

// TestAgentPollBlocksUntilWorkArrives covers long polling and the deadline.
func TestAgentPollBlocksUntilWorkArrives(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	token := agentCredential(t, controller, "linux-one")

	// With nothing queued, the poll returns empty after the long poll window.
	started := time.Now()
	recorder := tokenRequest(t, api, token, http.MethodPost, "/api/agent/v1/poll", AgentPollRequest{AgentID: "linux-one"})
	requireStatus(t, recorder, http.StatusOK)
	elapsed := time.Since(started)
	var empty AgentPollResponse
	decodeBody(t, recorder, &empty)
	if empty.Assignment != nil {
		t.Fatal("expected no assignment")
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("the poll must hold the connection, returned after %s", elapsed)
	}

	// Work queued during a poll wakes it up quickly.
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = controller.Enqueue("android-build", parameterValuesToQuery(map[string]string{"project": "alpha"}), "test")
	}()
	started = time.Now()
	recorder = tokenRequest(t, api, token, http.MethodPost, "/api/agent/v1/poll", AgentPollRequest{AgentID: "linux-one"})
	requireStatus(t, recorder, http.StatusOK)
	var filled AgentPollResponse
	decodeBody(t, recorder, &filled)
	if filled.Assignment == nil {
		t.Fatalf("expected the poll to be woken by new work, waited %s", time.Since(started))
	}
}

func TestAgentHeartbeatReportsCancellationAndOffset(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	token := agentCredential(t, controller, "linux-one")
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	body := []byte("output\n")
	if _, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: execution.ID, Data: body}); err != nil {
		t.Fatal(err)
	}

	recorder := tokenRequest(t, api, token, http.MethodPost, "/api/agent/v1/heartbeat",
		AgentHeartbeatRequest{AgentID: "linux-one", ExecutionID: execution.ID, State: agentStateRunning})
	requireStatus(t, recorder, http.StatusOK)
	var response AgentHeartbeatResponse
	decodeBody(t, recorder, &response)
	if !response.Known || response.CancelRequested {
		t.Fatalf("unexpected heartbeat response %+v", response)
	}
	if response.AckOffset != int64(len(body)) {
		t.Fatalf("the heartbeat must report the durable offset, got %d", response.AckOffset)
	}
	if response.Status != StatusRunning {
		t.Fatalf("expected the status in the heartbeat, got %q", response.Status)
	}

	if _, err := controller.Cancel(execution.ID); err != nil {
		t.Fatal(err)
	}
	recorder = tokenRequest(t, api, token, http.MethodPost, "/api/agent/v1/heartbeat",
		AgentHeartbeatRequest{AgentID: "linux-one", ExecutionID: execution.ID, State: agentStateRunning})
	requireStatus(t, recorder, http.StatusOK)
	decodeBody(t, recorder, &response)
	if !response.CancelRequested {
		t.Fatal("the heartbeat must deliver a pending cancellation")
	}

	// An unknown execution is reported as unknown rather than as an error.
	recorder = tokenRequest(t, api, token, http.MethodPost, "/api/agent/v1/heartbeat",
		AgentHeartbeatRequest{AgentID: "linux-one", ExecutionID: "missing"})
	requireStatus(t, recorder, http.StatusOK)
	decodeBody(t, recorder, &response)
	if response.Known {
		t.Fatal("an unknown execution must not be reported as known")
	}
}

// TestLogUploadOffsetsAreOrderedIdempotentAndGapSafe is the core log transport
// test: duplicates are no-ops, a gap is refused with the current offset, and
// a retransmission converges.
func TestLogUploadOffsetsAreOrderedIdempotentAndGapSafe(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	id := execution.ID

	first := []byte("aaaa")
	acked, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: id, Offset: 0, Data: first})
	if err != nil {
		t.Fatal(err)
	}
	if acked != 4 {
		t.Fatalf("expected 4 acknowledged bytes, got %d", acked)
	}

	// Exact duplicate.
	acked, err = controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: id, Offset: 0, Data: first})
	if err != nil {
		t.Fatal(err)
	}
	if acked != 4 {
		t.Fatalf("a duplicate chunk must not grow the log, got %d", acked)
	}

	// Overlapping chunk: only the new tail is appended.
	acked, err = controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: id, Offset: 2, Data: []byte("aabb")})
	if err != nil {
		t.Fatal(err)
	}
	if acked != 6 {
		t.Fatalf("expected 6 bytes after the overlap, got %d", acked)
	}

	// A chunk past the durable end is refused with the current offset so the
	// agent retransmits instead of writing a gap.
	acked, err = controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: id, Offset: 100, Data: []byte("zzz")})
	if err != nil {
		t.Fatal(err)
	}
	if acked != 6 {
		t.Fatalf("a gap must be refused with the current offset, got %d", acked)
	}

	// Retransmission from the reported offset succeeds.
	acked, err = controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: id, Offset: 6, Data: []byte("cc")})
	if err != nil {
		t.Fatal(err)
	}
	if acked != 8 {
		t.Fatalf("expected 8 bytes, got %d", acked)
	}

	data, err := os.ReadFile(executionLogPath(controller.Runtime().LogDir, id))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "aaaabbcc" {
		t.Fatalf("unexpected log contents %q", data)
	}
	info, err := os.Stat(executionLogPath(controller.Runtime().LogDir, id))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("logs must be written with mode 0600, got %v", info.Mode().Perm())
	}
}

// TestResultOnlyAcceptedWithCompleteLogs proves a result is confirmed only
// once every log byte has arrived.
func TestResultOnlyAcceptedWithCompleteLogs(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: execution.ID, Data: []byte("half")}); err != nil {
		t.Fatal(err)
	}

	response, err := controller.SubmitResult("linux-one", AgentResultRequest{
		ExecutionID: execution.ID, Status: StatusSuccess, LogLength: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Accepted {
		t.Fatal("a result must not be accepted while log bytes are missing")
	}
	if response.AckOffset != 4 {
		t.Fatalf("the refusal must report the durable offset, got %d", response.AckOffset)
	}
	if statusOf(t, controller, execution.ID) != StatusRunning {
		t.Fatal("a refused result must not finish the execution")
	}

	if _, err := controller.AppendLog("linux-one", AgentLogRequest{ExecutionID: execution.ID, Offset: 4, Data: []byte("done!!")}); err != nil {
		t.Fatal(err)
	}
	response, err = controller.SubmitResult("linux-one", AgentResultRequest{
		ExecutionID: execution.ID, Status: StatusSuccess, LogLength: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Accepted {
		t.Fatalf("the result must be accepted once the log is complete: %s", response.Reason)
	}
	stored := mustFind(t, controller, execution.ID)
	if !stored.LogComplete || stored.LogOffset != 10 {
		t.Fatalf("unexpected final record %+v", stored)
	}
}

func TestResultRejectsInvalidStatusAndForeignAgent(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	if _, err := controller.SubmitResult("linux-two", AgentResultRequest{ExecutionID: execution.ID, Status: StatusSuccess}); err == nil {
		t.Fatal("expected a foreign agent to be refused")
	}
	if _, err := controller.SubmitResult("linux-one", AgentResultRequest{ExecutionID: execution.ID, Status: "WEIRD"}); err == nil {
		t.Fatal("expected an invalid status to be refused")
	}
	if _, err := controller.SubmitResult("linux-one", AgentResultRequest{ExecutionID: "missing", Status: StatusSuccess}); err == nil {
		t.Fatal("expected an unknown execution to be refused")
	}
}

// TestReconcileRequeuesOnlyProvablyUnstartedWork is the uncertainty rule: no
// execution whose start permit was granted is ever reassigned.
func TestReconcileRequeuesOnlyProvablyUnstartedWork(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")

	unstarted := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if statusOf(t, controller, unstarted.ID) != StatusAssigned {
		t.Fatal("precondition: the execution must be assigned")
	}
	// The agent reports it knows nothing, and no permit was ever granted.
	unknown, err := controller.Reconcile("linux-one", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != unstarted.ID {
		t.Fatalf("unexpected unknown list %v", unknown)
	}
	requeued := mustFind(t, controller, unstarted.ID)
	if requeued.Status != StatusQueued || requeued.AgentID != "" {
		t.Fatalf("an execution that never got a permit is safe to requeue, got %+v", requeued)
	}

	// Now grant a permit and repeat: the execution must be escalated instead.
	if err := controller.scheduleOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.GrantPermit("linux-one", unstarted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Reconcile("linux-one", nil); err != nil {
		t.Fatal(err)
	}
	escalated := mustFind(t, controller, unstarted.ID)
	if escalated.Status != StatusRunning {
		t.Fatalf("an uncertain execution must not change state, got %q", escalated.Status)
	}
	if !escalated.NeedsAttention || escalated.Attention == "" {
		t.Fatalf("an uncertain execution must be escalated, got %+v", escalated)
	}
	if escalated.AgentID != "linux-one" {
		t.Fatal("ownership must be retained so nothing is reassigned")
	}
	// The agent is blocked and takes no new work.
	if agentOf(t, controller, "linux-one").schedulable() {
		t.Fatal("a blocked agent must not be schedulable")
	}
}

// TestReconcileKeepsWorkTheAgentStillTracks guards against needless churn.
func TestReconcileKeepsWorkTheAgentStillTracks(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	unknown, err := controller.Reconcile("linux-one", []string{execution.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("nothing should be unknown, got %v", unknown)
	}
	if statusOf(t, controller, execution.ID) != StatusAssigned {
		t.Fatal("the assignment must be left alone")
	}
}

// TestResolveAttentionIsAnOperatorDecision proves the controller never guesses
// the outcome of an unverifiable execution.
func TestResolveAttentionIsAnOperatorDecision(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	if err := controller.ReportAttention("linux-one", AgentAttentionRequest{
		ExecutionID: execution.ID, Message: "process group ownership could not be proven",
	}); err != nil {
		t.Fatal(err)
	}
	if !mustFind(t, controller, execution.ID).NeedsAttention {
		t.Fatal("the execution must be flagged")
	}
	// Until an operator acts, nothing changes.
	if statusOf(t, controller, execution.ID) != StatusRunning {
		t.Fatal("an escalated execution keeps its state")
	}

	recorder := request(t, api, session, http.MethodPost, "/api/runs/"+execution.ID+"/resolve?note=checked+by+hand", nil)
	requireStatus(t, recorder, http.StatusOK)
	resolved := mustFind(t, controller, execution.ID)
	if resolved.Status != StatusAborted || resolved.NeedsAttention {
		t.Fatalf("unexpected resolved execution %+v", resolved)
	}
	if resolved.Error != "checked by hand" {
		t.Fatalf("the operator note must be recorded, got %q", resolved.Error)
	}
	// Resolving twice is refused.
	requireStatus(t, request(t, api, session, http.MethodPost, "/api/runs/"+execution.ID+"/resolve", nil), http.StatusConflict)
	// And the agent is free again.
	if !agentOf(t, controller, "linux-one").schedulable() {
		t.Fatal("the agent must be schedulable once the execution is resolved")
	}
}
