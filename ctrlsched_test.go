package main

import (
	"testing"
	"time"
)

// agentOf finds one agent view by id.
func agentOf(t *testing.T, controller *Controller, id string) agentView {
	t.Helper()
	for _, view := range controller.AgentViews() {
		if view.Definition.ID == id {
			return view
		}
	}
	t.Fatalf("agent %s not found", id)
	return agentView{}
}

// assignedAgent returns the agent an execution landed on.
func assignedAgent(t *testing.T, controller *Controller, id string) string {
	t.Helper()
	execution, ok := controller.store.Find(id)
	if !ok {
		t.Fatalf("execution %s not found", id)
	}
	return execution.AgentID
}

func TestSchedulerRequiresEveryJobLabel(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one", "mac-one")

	android := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	ios := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	if got := assignedAgent(t, controller, android.ID); got != "linux-one" {
		t.Fatalf("android work must go to the linux agent, got %q", got)
	}
	if got := assignedAgent(t, controller, ios.ID); got != "mac-one" {
		t.Fatalf("ios work must go to the mac agent, got %q", got)
	}
}

// TestSchedulerRunsDifferentAgentsInParallel covers the single-slot rule per
// agent plus parallelism across agents.
func TestSchedulerRunsDifferentAgentsInParallel(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one", "linux-two")

	first := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	second := enqueue(t, controller, "android-build", map[string]string{"project": "beta"})
	third := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	agents := map[string]bool{}
	for _, id := range []string{first.ID, second.ID} {
		agent := assignedAgent(t, controller, id)
		if agent == "" {
			t.Fatalf("execution %s was not assigned", id)
		}
		agents[agent] = true
	}
	if len(agents) != 2 {
		t.Fatalf("two free agents must both receive work, got %v", agents)
	}
	if statusOf(t, controller, third.ID) != StatusQueued {
		t.Fatal("a third execution must wait because each agent runs one job at a time")
	}
}

// TestSchedulerAllowsSameProjectConcurrently is an explicit product decision:
// there is no project lock.
func TestSchedulerAllowsSameProjectConcurrently(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one", "linux-two")

	first := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	second := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	firstAgent := assignedAgent(t, controller, first.ID)
	secondAgent := assignedAgent(t, controller, second.ID)
	if firstAgent == "" || secondAgent == "" {
		t.Fatalf("both same-project executions must be assigned, got %q and %q", firstAgent, secondAgent)
	}
	if firstAgent == secondAgent {
		t.Fatalf("expected two different agents, both got %q", firstAgent)
	}
}

// TestSchedulerPrefersOldestLastAssignment pins the fairness rule and its
// agent-id tie break.
func TestSchedulerPrefersOldestLastAssignment(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one", "linux-two")

	// With no history both agents tie, so the lower agent id wins.
	first := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if got := assignedAgent(t, controller, first.ID); got != "linux-one" {
		t.Fatalf("the id tie break must pick linux-one, got %q", got)
	}

	// Finish that execution so linux-one is free again but now has the most
	// recent assignment.
	finishExecution(t, controller, first.ID, StatusSuccess)
	second := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if got := assignedAgent(t, controller, second.ID); got != "linux-two" {
		t.Fatalf("the agent with the oldest last assignment must win, got %q", got)
	}
}

// TestSchedulerDoesNotStallBehindBlockedWork proves a queued item that nothing
// can run never holds up the items behind it.
func TestSchedulerDoesNotStallBehindBlockedWork(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")

	blocked := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})
	runnable := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	if statusOf(t, controller, blocked.ID) != StatusQueued {
		t.Fatal("the ios execution has no online agent and must stay queued")
	}
	if got := assignedAgent(t, controller, runnable.ID); got != "linux-one" {
		t.Fatalf("the android execution behind it must still be assigned, got %q", got)
	}
}

func TestSchedulerSkipsOfflinePausedAndDisabledAgents(t *testing.T) {
	document := configDocumentWith("  - id: \"linux-two\"\n    labels: [\"linux\", \"android\"]\n",
		"  - id: \"linux-two\"\n    labels: [\"linux\", \"android\"]\n    paused: true\n")
	controller := newTestController(t, document)

	// Nobody is online yet.
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if statusOf(t, controller, execution.ID) != StatusQueued {
		t.Fatal("an offline fleet must leave work queued")
	}
	if reason := queueReason(controller.AgentViews(), execution.Labels); reason != QueueReasonOffline {
		t.Fatalf("expected the offline reason, got %q", reason)
	}

	// Only the paused agent is online.
	markOnline(controller, "linux-two")
	if err := controller.scheduleOnce(); err != nil {
		t.Fatal(err)
	}
	if statusOf(t, controller, execution.ID) != StatusQueued {
		t.Fatal("a paused agent must not receive new work")
	}
	if reason := queueReason(controller.AgentViews(), execution.Labels); reason != QueueReasonPaused {
		t.Fatalf("expected the paused reason, got %q", reason)
	}

	markOnline(controller, "linux-one")
	if err := controller.scheduleOnce(); err != nil {
		t.Fatal(err)
	}
	if got := assignedAgent(t, controller, execution.ID); got != "linux-one" {
		t.Fatalf("the unpaused agent must take the work, got %q", got)
	}
}

func TestQueueReasons(t *testing.T) {
	controller := newTestController(t, testControllerConfig)

	if reason := queueReason(controller.AgentViews(), []string{"windows"}); reason != QueueReasonNoMatchingLabels {
		t.Fatalf("expected no-matching-labels, got %q", reason)
	}
	markOnline(controller, "linux-one", "linux-two")
	if reason := queueReason(controller.AgentViews(), []string{"linux", "android"}); reason != QueueReasonReady {
		t.Fatalf("expected ready, got %q", reason)
	}
	enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	enqueue(t, controller, "android-build", map[string]string{"project": "beta"})
	if reason := queueReason(controller.AgentViews(), []string{"linux", "android"}); reason != QueueReasonBusy {
		t.Fatalf("expected busy once both agents hold work, got %q", reason)
	}
}

func TestQueueReasonDisabledAgent(t *testing.T) {
	document := "role: controller\njobs:\n  - id: j\n    script: x\n    labels: [linux]\nagents:\n  - id: a\n    labels: [linux]\n    enabled: false\n"
	controller := newTestController(t, document)
	markOnline(controller, "a")
	if reason := queueReason(controller.AgentViews(), []string{"linux"}); reason != QueueReasonDisabled {
		t.Fatalf("expected disabled, got %q", reason)
	}
	execution := enqueue(t, controller, "j", nil)
	if statusOf(t, controller, execution.ID) != StatusQueued {
		t.Fatal("a disabled agent must not receive work")
	}
}

func TestAgentViewReportsLivenessAndOccupancy(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	if agentOf(t, controller, "linux-one").Online {
		t.Fatal("an agent that never reported must be offline")
	}
	markOnline(controller, "linux-one")
	view := agentOf(t, controller, "linux-one")
	if !view.Online || view.Version != "test" {
		t.Fatalf("unexpected view %+v", view)
	}
	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	view = agentOf(t, controller, "linux-one")
	if !view.Busy || view.CurrentExecutionID != execution.ID || view.CurrentStatus != StatusAssigned {
		t.Fatalf("unexpected busy view %+v", view)
	}
	if view.LastAssignedAt.IsZero() {
		t.Fatal("the assignment time must be recorded for fairness")
	}
	if view.schedulable() {
		t.Fatal("a busy agent is not schedulable")
	}
}

// TestSchedulerIsIdempotent proves an idle controller does not rewrite state.
func TestSchedulerIsIdempotent(t *testing.T) {
	controller := newTestController(t, testControllerConfig)
	markOnline(controller, "linux-one")
	enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	var before uint64
	controller.store.read(func(st *controllerStateData) { before = st.Sequence })
	for i := 0; i < 3; i++ {
		if err := controller.scheduleOnce(); err != nil {
			t.Fatal(err)
		}
	}
	var after uint64
	controller.store.read(func(st *controllerStateData) { after = st.Sequence })
	if before != after {
		t.Fatalf("scheduling with nothing to do must not persist a new snapshot: %d -> %d", before, after)
	}
}

func TestOfflineAfterBoundary(t *testing.T) {
	document := configDocumentWith("offline_after: \"2s\"", "offline_after: \"30ms\"")
	controller := newTestController(t, document)
	markOnline(controller, "linux-one")
	if !controller.agentOnline("linux-one") {
		t.Fatal("a freshly seen agent must be online")
	}
	time.Sleep(60 * time.Millisecond)
	if controller.agentOnline("linux-one") {
		t.Fatal("an agent must go offline after the configured window")
	}
}
