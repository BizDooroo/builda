package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"strings"
	"time"
)

// principalKind identifies how a request authenticated.
const (
	principalSession = "session"
	principalAPI     = "api"
	principalAgent   = "agent"
)

// principal is the authenticated identity of one request.
type principal struct {
	Kind    string
	User    string
	AgentID string
	Session *Session
}

func (p principal) isAdmin() bool {
	return p.Kind == principalSession || p.Kind == principalAPI
}

// ControllerAPI serves the Web UI and every controller HTTP endpoint. There is
// no unauthenticated job, config, or log surface.
type ControllerAPI struct {
	controller *Controller
	sessions   *SessionStore
	limiter    *loginLimiter
}

func newControllerAPI(controller *Controller) *ControllerAPI {
	return &ControllerAPI{
		controller: controller,
		sessions:   newSessionStore(sessionTTL),
		limiter:    newLoginLimiter(loginFailureWindow, loginFailureLimit),
	}
}

func (s *ControllerAPI) routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated surface: the login page, its assets, and the login call.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		serveWebFile(w, r, "login/index.html")
	})

	admin := s.requireAdmin
	mux.Handle("GET /api/meta", admin(s.handleMeta))
	mux.Handle("GET /api/session", admin(s.handleSession))
	mux.Handle("POST /api/logout", admin(s.handleLogout))

	mux.Handle("GET /api/jobs", admin(s.handleListJobs))
	mux.Handle("POST /api/jobs", admin(s.handleCreateJob))
	mux.Handle("GET /api/jobs/{id}", admin(s.handleGetJob))
	mux.Handle("PUT /api/jobs/{id}", admin(s.handleUpdateJob))
	mux.Handle("DELETE /api/jobs/{id}", admin(s.handleDeleteJob))
	mux.Handle("POST /api/jobs/{id}/runs", admin(s.handleRunJob))

	mux.Handle("GET /api/catalogs", admin(s.handleListCatalogs))
	mux.Handle("POST /api/catalogs", admin(s.handleCreateCatalog))
	mux.Handle("GET /api/catalogs/{id}", admin(s.handleGetCatalog))
	mux.Handle("PUT /api/catalogs/{id}", admin(s.handleUpdateCatalog))
	mux.Handle("DELETE /api/catalogs/{id}", admin(s.handleDeleteCatalog))

	mux.Handle("GET /api/agents", admin(s.handleListAgents))
	mux.Handle("POST /api/agents", admin(s.handleCreateAgent))
	mux.Handle("GET /api/agents/{id}", admin(s.handleGetAgent))
	mux.Handle("PUT /api/agents/{id}", admin(s.handleUpdateAgent))
	mux.Handle("DELETE /api/agents/{id}", admin(s.handleDeleteAgent))
	mux.Handle("POST /api/agents/{id}/token", admin(s.handleRotateAgentToken))
	mux.Handle("DELETE /api/agents/{id}/token", admin(s.handleRevokeAgentToken))

	mux.Handle("GET /api/queue", admin(s.handleQueue))
	mux.Handle("POST /api/queue/cancel", admin(s.handleQueueCancel))

	mux.Handle("GET /api/runs", admin(s.handleListRuns))
	mux.Handle("GET /api/runs/{id}", admin(s.handleGetRun))
	mux.Handle("GET /api/runs/{id}/log", admin(s.handleRunLog))
	mux.Handle("POST /api/runs/{id}/cancel", admin(s.handleCancelRun))
	mux.Handle("POST /api/runs/{id}/rerun", admin(s.handleRerunRun))
	mux.Handle("POST /api/runs/{id}/resolve", admin(s.handleResolveRun))
	mux.Handle("DELETE /api/runs/{id}", admin(s.handleDeleteRun))

	mux.Handle("GET /api/config", admin(s.handleGetConfigDocument))
	mux.Handle("POST /api/config", admin(s.handleSaveConfigDocument))

	mux.Handle("GET /api/tokens", admin(s.handleListTokens))
	mux.Handle("POST /api/tokens", admin(s.handleCreateToken))
	mux.Handle("DELETE /api/tokens/{id}", admin(s.handleRevokeToken))

	agent := s.requireAgent
	mux.Handle("POST /api/agent/v1/poll", agent(s.handleAgentPoll))
	mux.Handle("POST /api/agent/v1/permit", agent(s.handleAgentPermit))
	mux.Handle("POST /api/agent/v1/heartbeat", agent(s.handleAgentHeartbeat))
	mux.Handle("POST /api/agent/v1/log", agent(s.handleAgentLog))
	mux.Handle("POST /api/agent/v1/result", agent(s.handleAgentResult))
	mux.Handle("POST /api/agent/v1/attention", agent(s.handleAgentAttention))

	mux.HandleFunc("/", s.handlePage)
	return mux
}

// handlerFunc is an authenticated handler.
type handlerFunc func(http.ResponseWriter, *http.Request, principal)

// requireAdmin authenticates a session or API token and enforces CSRF on
// browser state changes.
func (s *ControllerAPI) requireAdmin(next handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok := s.authenticate(r)
		if !ok || !who.isAdmin() {
			respondError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if who.Kind == principalSession && isStateChangingMethod(r.Method) {
			if err := s.checkCSRF(r, who); err != nil {
				respondError(w, http.StatusForbidden, err.Error())
				return
			}
		}
		next(w, r, who)
	})
}

// requireAgent authenticates a per-agent token and binds it to one identity.
func (s *ControllerAPI) requireAgent(next handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok := s.authenticate(r)
		if !ok || who.Kind != principalAgent {
			respondError(w, http.StatusUnauthorized, "agent token required")
			return
		}
		next(w, r, who)
	})
}

func (s *ControllerAPI) authenticate(r *http.Request) (principal, bool) {
	if secret := bearerToken(r); secret != "" {
		if record, ok := s.controller.auth.VerifyAgentToken(secret); ok {
			return principal{Kind: principalAgent, AgentID: record.AgentID}, true
		}
		if record, ok := s.controller.auth.VerifyAPIToken(secret); ok {
			name := record.Name
			if name == "" {
				name = "api-token"
			}
			return principal{Kind: principalAPI, User: name}, true
		}
		return principal{}, false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return principal{}, false
	}
	// A password rotation performed with the CLI must take effect here, so
	// sessions older than the current admin credential are dropped.
	if rotated := s.controller.auth.AdminUpdatedAt(); !rotated.IsZero() {
		s.sessions.DeleteBefore(rotated)
	}
	session, ok := s.sessions.Get(cookie.Value)
	if !ok {
		return principal{}, false
	}
	return principal{Kind: principalSession, User: session.User, Session: session}, true
}

// checkCSRF requires a matching CSRF token and a same-origin request for every
// browser-driven state change.
func (s *ControllerAPI) checkCSRF(r *http.Request, who principal) error {
	if who.Session == nil {
		return errors.New("session is missing")
	}
	token := r.Header.Get(csrfHeaderName)
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(who.Session.CSRF)) != 1 {
		return errors.New("CSRF token did not match")
	}
	return checkSameOrigin(r)
}

// checkSameOrigin rejects cross-site form posts even if a token leaks.
func checkSameOrigin(r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		// Browsers always send Origin on cross-site state changes; a missing
		// value is a same-origin navigation or a non-browser client.
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return errors.New("invalid Origin header")
	}
	if !strings.EqualFold(parsed.Host, r.Host) {
		return errors.New("cross-origin request rejected")
	}
	return nil
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// handlePage serves the embedded Web UI. Application pages require a session;
// static assets do not so the login page can render.
func (s *ControllerAPI) handlePage(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if isPublicAsset(path) {
		serveWebFile(w, r, path)
		return
	}
	// Pages are for the browser. An agent token authenticates the agent API
	// and has no business fetching the management UI.
	if who, ok := s.authenticate(r); !ok || who.Kind != principalSession {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			respondError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	page, ok := controllerPageFile(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveWebFile(w, r, page)
}

// controllerPageFile maps a UI route to its prebuilt static document.
func controllerPageFile(path string) (string, bool) {
	trimmed := strings.Trim(path, "/")
	switch trimmed {
	case "", "jobs":
		return "index.html", true
	case "catalogs", "agents", "queue", "runs", "settings":
		return trimmed + "/index.html", true
	}
	if rest, ok := strings.CutPrefix(trimmed, "runs/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return "run/index.html", true
	}
	return "", false
}

// isPublicAsset decides what an anonymous browser may fetch so the login page
// can render. It judges the cleaned path, so a traversal segment can never
// turn an asset prefix into a pass for an application page.
func isPublicAsset(requested string) bool {
	cleaned := strings.TrimPrefix(stdpath.Clean("/"+requested), "/")
	if cleaned != requested {
		return false
	}
	return strings.HasPrefix(cleaned, "_astro/") || cleaned == "favicon.svg"
}

// clientKey identifies a login client for rate limiting.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSONStatus(w, status, map[string]any{"ok": false, "error": message})
}

func decodeJSONBody(r *http.Request, target any, limit int64) error {
	if limit <= 0 {
		limit = 1 << 20
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func serveControllerHTTP(addrs []string, handler http.Handler) error {
	errCh := make(chan error, len(addrs))
	for _, addr := range addrs {
		addr := addr
		server := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 15 * time.Second,
		}
		go func() {
			log.Printf("listening on %s", addr)
			errCh <- server.ListenAndServe()
		}()
	}
	return <-errCh
}
