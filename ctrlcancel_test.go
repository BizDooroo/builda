package main

import (
	"errors"
	"strings"
	"testing"
)

func TestCancelQueuedExecutionIsImmediateAndPersisted(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	canceled, err := controller.Cancel(execution.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != StatusCanceled || canceled.FinishedAt.IsZero() || canceled.CanceledAt.IsZero() {
		t.Fatalf("unexpected canceled execution %+v", canceled)
	}
	reloaded := reloadController(t, controller)
	if statusOf(t, reloaded, execution.ID) != StatusCanceled {
		t.Fatal("a queued cancellation must survive a controller restart")
	}
}

// TestCancelAssignedRevokesThePermit proves the script provably never started:
// the permit is revoked in the same atomic mutation as the cancellation, so a
// later permit request is refused.
func TestCancelAssignedRevokesThePermit(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if statusOf(t, controller, execution.ID) != StatusAssigned {
		t.Fatalf("expected the execution to be assigned, got %q", statusOf(t, controller, execution.ID))
	}

	if _, err := controller.Cancel(execution.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	stored, _ := controller.store.Find(execution.ID)
	if stored.Status != StatusCanceled || !stored.PermitRevoked {
		t.Fatalf("an assigned cancellation must revoke the permit, got %+v", stored)
	}

	permit, err := controller.GrantPermit("linux-one", execution.ID)
	if err != nil {
		t.Fatalf("permit request: %v", err)
	}
	if permit.Granted {
		t.Fatal("a revoked permit must never be granted")
	}
	if !strings.Contains(permit.Reason, "cancellation") {
		t.Fatalf("expected a cancellation reason, got %q", permit.Reason)
	}
}

// TestPermitBeforeCancelKeepsExecutionRunning covers the other side of the
// race: once the permit is granted the controller must wait for the agent.
func TestPermitBeforeCancelKeepsExecutionRunning(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	permit, err := controller.GrantPermit("linux-one", execution.ID)
	if err != nil || !permit.Granted {
		t.Fatalf("permit: %+v %v", permit, err)
	}
	if statusOf(t, controller, execution.ID) != StatusRunning {
		t.Fatal("a granted permit moves the execution to RUNNING")
	}

	canceled, err := controller.Cancel(execution.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != StatusCanceling {
		t.Fatalf("a running cancellation must wait for the agent, got %q", canceled.Status)
	}
	if !canceled.CancelRequested {
		t.Fatal("the cancellation must be recorded for the agent to pick up")
	}
	// The agent still owns the slot until it confirms.
	if !agentOf(t, controller, "linux-one").Busy {
		t.Fatal("a canceling execution must keep holding the agent slot")
	}
}

// TestDuplicatePermitRequestsDoNotRestart proves a repeated permit request
// cannot produce a second run.
func TestDuplicatePermitRequestsDoNotRestart(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	first, err := controller.GrantPermit("linux-one", execution.ID)
	if err != nil || !first.Granted || first.AlreadyGranted {
		t.Fatalf("unexpected first permit %+v (%v)", first, err)
	}
	startedAt := mustFind(t, controller, execution.ID).StartedAt

	second, err := controller.GrantPermit("linux-one", execution.ID)
	if err != nil {
		t.Fatalf("second permit: %v", err)
	}
	if !second.AlreadyGranted {
		t.Fatal("a repeated permit must be reported as already granted")
	}
	if !mustFind(t, controller, execution.ID).StartedAt.Equal(startedAt) {
		t.Fatal("a repeated permit must not restart the execution clock")
	}
}

func TestPermitRejectsForeignAgent(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	if _, err := controller.GrantPermit("linux-two", execution.ID); !errors.Is(err, errNotExecutionOwner) {
		t.Fatalf("expected an ownership error, got %v", err)
	}
	if _, err := controller.GrantPermit("linux-one", "missing"); !errors.Is(err, errExecutionNotFound) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

// TestCancelWhileAgentOfflineStaysPending proves the controller never
// fabricates a completion for work it cannot reach.
func TestCancelWhileAgentOfflineStaysPending(t *testing.T) {
	document := configDocumentWith("offline_after: \"2s\"", "offline_after: \"20ms\"")
	controller := newTestController(t, document)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}

	// Let the agent fall offline, then cancel.
	waitFor(t, "the agent to go offline", func() bool { return !controller.agentOnline("linux-one") })
	canceled, err := controller.Cancel(execution.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != StatusCanceling {
		t.Fatalf("an offline cancellation must stay pending, got %q", canceled.Status)
	}
	if !agentOf(t, controller, "linux-one").Busy {
		t.Fatal("the slot must not be released while the outcome is unknown")
	}
	// The pending cancellation is durable.
	reloaded := reloadController(t, controller)
	stored := mustFind(t, reloaded, execution.ID)
	if stored.Status != StatusCanceling || !stored.CancelRequested {
		t.Fatalf("the pending cancellation must survive a restart, got %+v", stored)
	}
	// And a queued execution behind it is not assigned to the busy agent.
	queued := enqueue(t, reloaded, "android-build", map[string]string{"project": "alpha"})
	markOnline(reloaded, "linux-one")
	if err := reloaded.scheduleOnce(); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, reloaded, queued.ID) != StatusQueued {
		t.Fatal("a busy agent must not take new work while a cancellation is pending")
	}
}

func TestCancelFinalizesOnAgentConfirmation(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Cancel(execution.ID); err != nil {
		t.Fatal(err)
	}

	// The agent reports it killed the process group.
	response, err := controller.SubmitResult("linux-one", AgentResultRequest{
		ExecutionID: execution.ID,
		Status:      StatusCanceled,
		ExitCode:    -1,
		LogLength:   0,
	})
	if err != nil || !response.Accepted {
		t.Fatalf("result: %+v %v", response, err)
	}
	stored := mustFind(t, controller, execution.ID)
	if stored.Status != StatusCanceled || stored.FinishedAt.IsZero() {
		t.Fatalf("expected a final CANCELED execution, got %+v", stored)
	}
	if agentOf(t, controller, "linux-one").Busy {
		t.Fatal("the slot must be released once the agent confirms")
	}
}

// TestCancelRequestForcesCanceledEvenOnScriptFailure covers an agent that
// reports FAILED because the kill tore the script down.
func TestCancelRequestForcesCanceledEvenOnScriptFailure(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Cancel(execution.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.SubmitResult("linux-one", AgentResultRequest{
		ExecutionID: execution.ID, Status: StatusFailed, ExitCode: 137,
	}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, controller, execution.ID); got != StatusCanceled {
		t.Fatalf("a requested cancellation must win over a failure report, got %q", got)
	}
}

func TestDuplicateResultDeliveryIsIdempotent(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if _, err := controller.GrantPermit("linux-one", execution.ID); err != nil {
		t.Fatal(err)
	}
	request := AgentResultRequest{ExecutionID: execution.ID, Status: StatusSuccess, ExitCode: 0}
	if _, err := controller.SubmitResult("linux-one", request); err != nil {
		t.Fatal(err)
	}
	finishedAt := mustFind(t, controller, execution.ID).FinishedAt

	response, err := controller.SubmitResult("linux-one", request)
	if err != nil {
		t.Fatalf("a repeated result must be accepted: %v", err)
	}
	if !response.Accepted {
		t.Fatal("a repeated result must be acknowledged so the agent can clear its spool")
	}
	if !mustFind(t, controller, execution.ID).FinishedAt.Equal(finishedAt) {
		t.Fatal("a repeated result must not rewrite the terminal record")
	}
}

func TestCancelTerminalExecutionIsRejected(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	finishExecution(t, controller, execution.ID, StatusSuccess)

	if _, err := controller.Cancel(execution.ID); !errors.Is(err, errExecutionDone) {
		t.Fatalf("expected a terminal cancellation to be rejected, got %v", err)
	}
	if _, err := controller.Cancel("missing"); !errors.Is(err, errExecutionNotFound) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestCancelManyReportsPerExecutionOutcome(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	first := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
	second := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
	markOnline(controller, "mac-one")
	if err := controller.scheduleOnce(); err != nil {
		t.Fatal(err)
	}
	finishExecution(t, controller, first.ID, StatusSuccess)

	results := controller.CancelMany([]string{first.ID, second.ID, "missing"})
	if results[first.ID] != "already-finished" {
		t.Fatalf("expected the finished execution to report already-finished, got %q", results[first.ID])
	}
	if results[second.ID] != StatusCanceled {
		t.Fatalf("expected the queued execution to be canceled, got %q", results[second.ID])
	}
	if results["missing"] != "not-found" {
		t.Fatalf("expected a not-found outcome, got %q", results["missing"])
	}
}
