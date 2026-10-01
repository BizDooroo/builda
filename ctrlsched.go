package main

import (
	"sort"
	"time"
)

// agentView is the combined configuration, liveness, and occupancy view of one
// agent used by the scheduler, the queue inspector, and the agents API.
type agentView struct {
	Definition         AgentDefinition
	Online             bool
	Busy               bool
	Blocked            bool
	BlockedReason      string
	CurrentExecutionID string
	CurrentStatus      string
	LastAssignedAt     time.Time
	LastSeen           time.Time
	Version            string
}

func (v agentView) schedulable() bool {
	return v.Online && v.Definition.IsEnabled() && !v.Definition.Paused && !v.Busy && !v.Blocked
}

// occupancy summarizes which agents are currently holding a slot or blocked.
type occupancy struct {
	busy    map[string]string
	status  map[string]string
	blocked map[string]string
}

func computeOccupancy(st *controllerStateData) occupancy {
	result := occupancy{busy: map[string]string{}, status: map[string]string{}, blocked: map[string]string{}}
	for _, execution := range st.Executions {
		if execution == nil || execution.AgentID == "" {
			continue
		}
		if isExecutionTerminal(execution.Status) {
			continue
		}
		if isExecutionActive(execution.Status) {
			result.busy[execution.AgentID] = execution.ID
			result.status[execution.AgentID] = execution.Status
		}
		if execution.NeedsAttention {
			result.blocked[execution.AgentID] = execution.Attention
		}
	}
	return result
}

// AgentViews returns the current view of every configured agent.
func (c *Controller) AgentViews() []agentView {
	cfg := c.Config()
	liveness := c.agentLivenessSnapshot()
	offlineAfter := c.Runtime().OfflineAfter

	var occ occupancy
	states := map[string]*AgentState{}
	c.store.read(func(st *controllerStateData) {
		occ = computeOccupancy(st)
		for id, state := range st.Agents {
			states[id] = state.clone()
		}
	})

	views := make([]agentView, 0, len(cfg.Agents))
	now := time.Now()
	for _, definition := range cfg.Agents {
		live := liveness[definition.ID]
		view := agentView{
			Definition:     definition,
			Online:         !live.lastSeen.IsZero() && now.Sub(live.lastSeen) < offlineAfter,
			LastSeen:       live.lastSeen,
			Version:        live.version,
			CurrentStatus:  occ.status[definition.ID],
			LastAssignedAt: time.Time{},
		}
		if state, ok := states[definition.ID]; ok && state != nil {
			view.LastAssignedAt = state.LastAssignedAt
		}
		if id, ok := occ.busy[definition.ID]; ok {
			view.Busy = true
			view.CurrentExecutionID = id
		}
		if reason, ok := occ.blocked[definition.ID]; ok {
			view.Blocked = true
			view.BlockedReason = reason
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		return views[i].Definition.ID < views[j].Definition.ID
	})
	return views
}

// EligibleAgents lists the agents whose labels satisfy every required label.
func EligibleAgents(views []agentView, required []string) []agentView {
	eligible := make([]agentView, 0, len(views))
	for _, view := range views {
		if labelsSatisfied(required, view.Definition.Labels) {
			eligible = append(eligible, view)
		}
	}
	return eligible
}

// queueReason explains why a queued execution has not been assigned. Online
// agents are described first, because an operator can act on a paused or busy
// agent immediately, while an offline one is a fleet problem.
func queueReason(views []agentView, required []string) string {
	matching := EligibleAgents(views, required)
	if len(matching) == 0 {
		return QueueReasonNoMatchingLabels
	}
	var onlineBusy, onlinePaused, onlineDisabled, offlineUsable, offlinePaused bool
	for _, view := range matching {
		enabled := view.Definition.IsEnabled()
		switch {
		case view.Online && enabled && !view.Definition.Paused:
			if !view.Busy && !view.Blocked {
				return QueueReasonReady
			}
			onlineBusy = true
		case view.Online && enabled:
			onlinePaused = true
		case view.Online:
			onlineDisabled = true
		case enabled && !view.Definition.Paused:
			offlineUsable = true
		case enabled:
			offlinePaused = true
		}
	}
	switch {
	case onlineBusy:
		return QueueReasonBusy
	case onlinePaused:
		return QueueReasonPaused
	case onlineDisabled:
		return QueueReasonDisabled
	case offlineUsable:
		return QueueReasonOffline
	case offlinePaused:
		return QueueReasonPaused
	default:
		return QueueReasonDisabled
	}
}

// scheduleOnce assigns as many queued executions as possible in a single
// atomic state mutation. Queue items are considered in enqueue order so a
// blocked item never stalls the items behind it, and among eligible agents the
// one with the oldest last assignment wins, breaking ties by agent ID.
//
// Occupancy is recomputed from the state inside the transaction. Reusing the
// view captured before the lock would let two concurrent calls hand the same
// agent two executions: the second caller holds a snapshot taken before the
// first one committed, and an agent only ever runs the first of them, so the
// rest would sit outside the queue where nothing can pick them up.
func (c *Controller) scheduleOnce() error {
	views := c.AgentViews()
	available := map[string]agentView{}
	for _, view := range views {
		// Liveness and configuration are not part of the persisted state, so
		// they are judged here; occupancy is judged in the transaction.
		if view.Online && view.Definition.IsEnabled() && !view.Definition.Paused {
			available[view.Definition.ID] = view
		}
	}
	if len(available) == 0 {
		return nil
	}

	assigned := false
	err := c.store.mutate(func(st *controllerStateData) error {
		occupied := computeOccupancy(st)
		free := map[string]bool{}
		lastAssigned := map[string]time.Time{}
		for id := range available {
			if _, busy := occupied.busy[id]; busy {
				continue
			}
			if _, blocked := occupied.blocked[id]; blocked {
				continue
			}
			free[id] = true
			if state, ok := st.Agents[id]; ok && state != nil {
				lastAssigned[id] = state.LastAssignedAt
			}
		}
		if len(free) == 0 {
			return errNoStateChange
		}
		queued := make([]*Execution, 0, len(st.Executions))
		for _, execution := range st.Executions {
			if execution != nil && execution.Status == StatusQueued && !execution.CancelRequested {
				queued = append(queued, execution)
			}
		}
		sort.SliceStable(queued, func(i, j int) bool {
			return queued[i].RequestedAt.Before(queued[j].RequestedAt)
		})

		now := time.Now()
		for _, execution := range queued {
			candidates := make([]string, 0, len(free))
			for id := range free {
				if labelsSatisfied(execution.Labels, available[id].Definition.Labels) {
					candidates = append(candidates, id)
				}
			}
			if len(candidates) == 0 {
				continue
			}
			sort.Slice(candidates, func(i, j int) bool {
				left, right := lastAssigned[candidates[i]], lastAssigned[candidates[j]]
				if !left.Equal(right) {
					return left.Before(right)
				}
				return candidates[i] < candidates[j]
			})
			chosen := candidates[0]
			execution.Status = StatusAssigned
			execution.AgentID = chosen
			execution.AssignedAt = now
			st.agent(chosen).LastAssignedAt = now
			delete(free, chosen)
			assigned = true
			if len(free) == 0 {
				break
			}
		}
		if !assigned {
			return errNoStateChange
		}
		return nil
	})
	if err == errNoStateChange {
		return nil
	}
	return err
}

// errNoStateChange aborts a mutation that would not change anything, so an
// idle controller never rewrites its state snapshot.
var errNoStateChange = errSentinel("no state change")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
