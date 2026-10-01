package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakyProxy sits between an agent and the controller so a test can drop
// specific responses, which is what a lost reply looks like to the agent.
type flakyProxy struct {
	target  http.Handler
	dropped atomic.Int32
	dropFor atomic.Value // string path suffix to drop once
}

func (p *flakyProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	suffix, _ := p.dropFor.Load().(string)
	if suffix != "" && strings.HasSuffix(r.URL.Path, suffix) {
		// Let the controller apply the change, then throw the response away.
		recorder := httptest.NewRecorder()
		p.target.ServeHTTP(recorder, r)
		p.dropFor.Store("")
		p.dropped.Add(1)
		hijackAbort(w)
		return
	}
	p.target.ServeHTTP(w, r)
}

// hijackAbort ends the response without a usable body so the client sees a
// transport failure rather than a reply.
func hijackAbort(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	_ = conn.Close()
}

// TestLostPermitResponseIsRecovered is the regression guard for an execution
// stranded forever: the controller granted the start permit and the reply was
// lost, so the agent had an accepted assignment the controller would never
// re-deliver because it no longer considered the execution assignable.
func TestLostPermitResponseIsRecovered(t *testing.T) {
	controller := newTestController(t, scriptedConfig("echo recovered", "60s"))
	proxy := &flakyProxy{target: newControllerAPI(controller).routes()}
	proxy.dropFor.Store("/api/agent/v1/permit")
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	go controller.schedulerLoop(20 * time.Millisecond)

	harness := newAgentHarness(t, controller, server.URL, "linux-one")
	harness.start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})

	waitFor(t, "the permit response to be dropped", func() bool { return proxy.dropped.Load() > 0 })
	// The controller granted the permit, so it believes the run started.
	waitFor(t, "the controller to mark the execution running", func() bool {
		return statusOf(t, controller, execution.ID) == StatusRunning
	})

	// The agent must notice it still owns the assignment and finish it.
	waitFor(t, "the agent to recover and finish the run", func() bool {
		return statusOf(t, controller, execution.ID) == StatusSuccess
	})
	if !strings.Contains(readControllerLog(t, controller, execution.ID), "recovered") {
		t.Fatal("the recovered run must have produced its output")
	}
	waitFor(t, "the agent spool to clear", func() bool {
		return len(harness.agent.journal.IDs()) == 0
	})
	// And the agent takes new work afterwards.
	next := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the next run to finish", func() bool {
		return statusOf(t, controller, next.ID) == StatusSuccess
	})
}

// TestPermanentResultRefusalEscalates proves the agent stops retrying a
// refusal that cannot succeed, keeps the evidence, and asks for attention
// instead of spinning forever.
func TestPermanentResultRefusalEscalates(t *testing.T) {
	controller := newTestController(t, scriptedConfig("echo done", "60s"))
	api := newControllerAPI(controller)
	refuse := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() && strings.HasSuffix(r.URL.Path, "/api/agent/v1/result") {
			respondError(w, http.StatusNotFound, "execution not found")
			return
		}
		api.routes().ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	go controller.schedulerLoop(20 * time.Millisecond)

	harness := newAgentHarness(t, controller, server.URL, "linux-one")
	refuse.Store(true)
	harness.start(t)

	execution := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	waitFor(t, "the agent to block on the refusal", func() bool {
		return harness.agent.isBlocked()
	})

	// The run is not reported as finished, the local evidence is kept, and
	// the agent accepts no new work.
	if isExecutionTerminal(statusOf(t, controller, execution.ID)) {
		t.Fatal("a refused result must not be reported as a completed run")
	}
	entry, err := harness.agent.journal.Load(execution.ID)
	if err != nil {
		t.Fatalf("the spool must be kept for inspection: %v", err)
	}
	if entry.Phase != journalBlocked || !strings.Contains(entry.Attention, "refused the result") {
		t.Fatalf("expected a blocked entry explaining the refusal, got %+v", entry)
	}
	if !strings.Contains(entry.Attention, harness.agent.journal.entryDir(execution.ID)) {
		t.Fatalf("the operator needs the spool path, got %q", entry.Attention)
	}

	queued := enqueue(t, controller, "android-build", map[string]string{"project": "alpha"})
	if !sleepFor(300 * time.Millisecond) {
		t.Fatal("unexpected interruption")
	}
	if statusOf(t, controller, queued.ID) == StatusSuccess {
		t.Fatal("a blocked agent must not take new work")
	}
}

func sleepFor(d time.Duration) bool {
	time.Sleep(d)
	return true
}
