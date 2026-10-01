package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Controller owns jobs, catalogs, agent definitions, the central queue, and
// the run history. Agents never listen on a port; they connect outbound.
type Controller struct {
	mu          sync.RWMutex
	cfg         ControllerConfig
	runtime     ControllerRuntime
	configStamp fileStamp

	store *ControllerStore
	auth  *AuthStore

	configEditMu sync.Mutex

	liveMu sync.Mutex
	live   map[string]*agentLiveness

	hostname string
	started  time.Time
	stop     chan struct{}
	stopOnce sync.Once
}

// agentLiveness is in-memory only. It is rebuilt from agent traffic and never
// persisted, so a controller restart cannot report a dead agent as online.
type agentLiveness struct {
	lastSeen time.Time
	version  string
	pollSeen time.Time
}

func newController(configPath string, cfg ControllerConfig, listenOverride []string) (*Controller, error) {
	runtime, err := controllerRuntime(configPath, cfg)
	if err != nil {
		return nil, err
	}
	if len(listenOverride) > 0 {
		runtime.ListenAddresses = normalizeListenAddresses(listenOverride)
	}
	if err := os.MkdirAll(runtime.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	store, err := newControllerStore(runtime.StatePath, runtime.LogDir, runtime.MaxHistory)
	if err != nil {
		return nil, err
	}
	auth, err := newAuthStore(runtime.CredentialsPath)
	if err != nil {
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	controller := &Controller{
		cfg:      cfg,
		runtime:  runtime,
		store:    store,
		auth:     auth,
		live:     map[string]*agentLiveness{},
		hostname: hostname,
		started:  time.Now(),
		stop:     make(chan struct{}),
	}
	if stamp, err := statFileStamp(configPath); err == nil {
		controller.configStamp = stamp
	}
	return controller, nil
}

func (c *Controller) Close() {
	c.stopOnce.Do(func() { close(c.stop) })
}

func (c *Controller) Config() ControllerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

func (c *Controller) Runtime() ControllerRuntime {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.runtime
}

func (c *Controller) applyConfig(cfg ControllerConfig, stamp fileStamp) error {
	runtime, err := controllerRuntime(c.runtime.ConfigPath, cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	runtime.ListenAddresses = c.runtime.ListenAddresses
	c.cfg = cfg
	c.runtime = runtime
	c.configStamp = stamp
	c.mu.Unlock()
	return c.store.SetMaxHistory(runtime.MaxHistory)
}

// reloadConfigIfChanged hot-reloads the controller config file so job, catalog,
// and agent edits take effect without a restart.
func (c *Controller) reloadConfigIfChanged() error {
	c.mu.RLock()
	path := c.runtime.ConfigPath
	previous := c.configStamp
	c.mu.RUnlock()
	if strings.TrimSpace(path) == "" {
		return nil
	}
	stamp, err := statFileStamp(path)
	if err != nil {
		return err
	}
	if stamp.equal(previous) {
		return nil
	}
	cfg, err := loadControllerConfig(path)
	if err != nil {
		return err
	}
	if err := c.applyConfig(cfg, stamp); err != nil {
		return err
	}
	log.Printf("reloaded controller config %s", path)
	return nil
}

// replaceConfig validates, persists, and applies a new controller config. The
// guard stamp rejects a concurrent edit that landed after the caller read.
func (c *Controller) replaceConfig(cfg ControllerConfig, guard fileStamp, enforceGuard bool) error {
	c.mu.RLock()
	path := c.runtime.ConfigPath
	current := c.configStamp
	c.mu.RUnlock()

	if enforceGuard {
		onDisk, err := statFileStamp(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!onDisk.equal(current) || !onDisk.equal(guard)) {
			return errConfigChanged
		}
	}
	data, err := marshalControllerConfig(cfg)
	if err != nil {
		return err
	}
	// Re-parse so the stored document is always one the loader accepts.
	normalized, err := parseControllerConfig(data)
	if err != nil {
		return err
	}
	data, err = marshalControllerConfig(normalized)
	if err != nil {
		return err
	}
	if err := writeFileAtomicSync(path, data, 0o600); err != nil {
		return err
	}
	stamp, _ := statFileStamp(path)
	return c.applyConfig(normalized, stamp)
}

var errConfigChanged = errors.New("controller config changed since it was read")

// editConfig serializes config mutations so two admins cannot clobber each
// other. It first picks up any edit made directly to the file so the change is
// applied on top of the current document, and it refuses to write if the file
// changes again between that read and the write.
func (c *Controller) editConfig(fn func(cfg *ControllerConfig) error) error {
	c.configEditMu.Lock()
	defer c.configEditMu.Unlock()
	if err := c.reloadConfigIfChanged(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reload controller config before editing: %w", err)
	}
	next := cloneControllerConfig(c.Config())
	if err := fn(&next); err != nil {
		return err
	}
	c.mu.RLock()
	guard := c.configStamp
	c.mu.RUnlock()
	return c.replaceConfig(next, guard, true)
}

func cloneControllerConfig(cfg ControllerConfig) ControllerConfig {
	next := ControllerConfig{Role: RoleController, Server: cfg.Server}
	next.Server.Addresses = append([]string(nil), cfg.Server.Addresses...)
	for _, catalog := range cfg.Catalogs {
		copied := catalog
		copied.Options = make([]OptionConfig, 0, len(catalog.Options))
		for _, option := range catalog.Options {
			copied.Options = append(copied.Options, cloneOption(option))
		}
		next.Catalogs = append(next.Catalogs, copied)
	}
	for _, job := range cfg.Jobs {
		next.Jobs = append(next.Jobs, cloneJobConfig(job))
	}
	for _, agent := range cfg.Agents {
		copied := agent
		copied.Labels = append([]string(nil), agent.Labels...)
		if agent.Enabled != nil {
			copied.Enabled = boolPointer(*agent.Enabled)
		}
		next.Agents = append(next.Agents, copied)
	}
	return next
}

// markAgentSeen records agent liveness from any authenticated agent request.
func (c *Controller) markAgentSeen(agentID, version string) {
	now := time.Now()
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	state, ok := c.live[agentID]
	if !ok {
		state = &agentLiveness{}
		c.live[agentID] = state
	}
	state.lastSeen = now
	if strings.TrimSpace(version) != "" {
		state.version = version
	}
}

func (c *Controller) agentLivenessSnapshot() map[string]agentLiveness {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	snapshot := make(map[string]agentLiveness, len(c.live))
	for id, state := range c.live {
		snapshot[id] = *state
	}
	return snapshot
}

func (c *Controller) agentOnline(agentID string) bool {
	offlineAfter := c.Runtime().OfflineAfter
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	state, ok := c.live[agentID]
	if !ok {
		return false
	}
	return time.Since(state.lastSeen) < offlineAfter
}
