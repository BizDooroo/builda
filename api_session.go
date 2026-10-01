package main

import (
	"errors"
	"net/http"
	"strings"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin authenticates the single admin account. Failed attempts are rate
// limited per client address.
func (s *ControllerAPI) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := checkSameOrigin(r); err != nil {
		respondError(w, http.StatusForbidden, err.Error())
		return
	}
	key := clientKey(r)
	if !s.limiter.Allowed(key) {
		respondError(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
		return
	}
	var request loginRequest
	if err := decodeJSONBody(r, &request, 8<<10); err != nil {
		respondError(w, http.StatusBadRequest, "invalid login request")
		return
	}
	if err := s.controller.auth.VerifyPassword(request.Username, request.Password); err != nil {
		if errors.Is(err, errNoAdminCredential) {
			respondError(w, http.StatusServiceUnavailable, errNoAdminCredential.Error())
			return
		}
		if s.limiter.Fail(key) {
			respondError(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
			return
		}
		respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	s.limiter.Reset(key)
	session, err := s.sessions.Create(strings.TrimSpace(request.Username))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  session.Expires,
	})
	respondJSON(w, map[string]any{"ok": true, "user": session.User, "csrf": session.CSRF})
}

func (s *ControllerAPI) handleLogout(w http.ResponseWriter, r *http.Request, who principal) {
	if who.Session != nil {
		s.sessions.Delete(who.Session.ID)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	respondJSON(w, map[string]any{"ok": true})
}

// handleSession returns the CSRF token the browser must echo on every state
// change. API-token callers do not use CSRF.
func (s *ControllerAPI) handleSession(w http.ResponseWriter, r *http.Request, who principal) {
	payload := map[string]any{"kind": who.Kind, "user": who.User}
	if who.Session != nil {
		payload["csrf"] = who.Session.CSRF
		payload["expires"] = who.Session.Expires
	}
	respondJSON(w, payload)
}

// handleMeta reports build and controller identity for the Web UI shell.
func (s *ControllerAPI) handleMeta(w http.ResponseWriter, r *http.Request, who principal) {
	build := currentVersionDetails()
	runtime := s.controller.Runtime()
	commit := build.Commit
	if commit == "" {
		commit = "unknown"
	}
	buildDate := build.BuildDate
	if buildDate == "" {
		buildDate = "unknown"
	}
	respondJSON(w, map[string]any{
		"role":               RoleController,
		"hostname":           s.controller.hostname,
		"started":            s.controller.started.Format(displayTimeLayout),
		"config_path":        runtime.ConfigPath,
		"state_dir":          runtime.StateDir,
		"max_history":        runtime.MaxHistory,
		"heartbeat_interval": runtime.HeartbeatInterval.String(),
		"offline_after":      runtime.OfflineAfter.String(),
		"long_poll_timeout":  runtime.LongPollTimeout.String(),
		"user":               who.User,
		"version":            build.Version,
		"commit":             commit,
		"build_date":         buildDate,
		"build_modified":     build.Modified,
		"version_info":       versionInfo(),
	})
}

// requestIsSecure reports whether the browser reached the controller over TLS,
// directly or through a terminating proxy.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
