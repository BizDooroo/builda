package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// protectedEndpoints is every surface that must refuse an anonymous caller.
func protectedEndpoints() [][2]string {
	return [][2]string{
		{http.MethodGet, "/api/meta"},
		{http.MethodGet, "/api/session"},
		{http.MethodGet, "/api/jobs"},
		{http.MethodPost, "/api/jobs"},
		{http.MethodGet, "/api/jobs/android-build"},
		{http.MethodPut, "/api/jobs/android-build"},
		{http.MethodDelete, "/api/jobs/android-build"},
		{http.MethodPost, "/api/jobs/android-build/runs"},
		{http.MethodGet, "/api/catalogs"},
		{http.MethodPost, "/api/catalogs"},
		{http.MethodGet, "/api/catalogs/projects"},
		{http.MethodPut, "/api/catalogs/projects"},
		{http.MethodDelete, "/api/catalogs/projects"},
		{http.MethodGet, "/api/agents"},
		{http.MethodPost, "/api/agents"},
		{http.MethodGet, "/api/agents/linux-one"},
		{http.MethodPut, "/api/agents/linux-one"},
		{http.MethodDelete, "/api/agents/linux-one"},
		{http.MethodPost, "/api/agents/linux-one/token"},
		{http.MethodDelete, "/api/agents/linux-one/token"},
		{http.MethodGet, "/api/queue"},
		{http.MethodPost, "/api/queue/cancel"},
		{http.MethodGet, "/api/runs"},
		{http.MethodGet, "/api/runs/x"},
		{http.MethodGet, "/api/runs/x/log"},
		{http.MethodPost, "/api/runs/x/cancel"},
		{http.MethodPost, "/api/runs/x/rerun"},
		{http.MethodPost, "/api/runs/x/resolve"},
		{http.MethodDelete, "/api/runs/x"},
		{http.MethodGet, "/api/config"},
		{http.MethodPost, "/api/config"},
		{http.MethodGet, "/api/tokens"},
		{http.MethodPost, "/api/tokens"},
		{http.MethodDelete, "/api/tokens/x"},
		{http.MethodPost, "/api/agent/v1/poll"},
		{http.MethodPost, "/api/agent/v1/permit"},
		{http.MethodPost, "/api/agent/v1/heartbeat"},
		{http.MethodPost, "/api/agent/v1/log"},
		{http.MethodPost, "/api/agent/v1/result"},
		{http.MethodPost, "/api/agent/v1/attention"},
	}
}

// TestEveryApiRequiresAuthentication keeps job, config, and log surfaces from
// ever being reachable anonymously.
func TestEveryApiRequiresAuthentication(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	for _, endpoint := range protectedEndpoints() {
		req := httptest.NewRequest(endpoint[0], endpoint[1], strings.NewReader("{}"))
		recorder := httptest.NewRecorder()
		api.routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s returned %d, expected 401: %s", endpoint[0], endpoint[1], recorder.Code, recorder.Body.String())
		}
	}
}

// TestPagesRedirectAnonymousBrowsersToLogin keeps the UI behind the session
// while leaving the login page and its assets reachable.
func TestPagesRedirectAnonymousBrowsersToLogin(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	for _, path := range []string{"/", "/jobs", "/catalogs", "/agents", "/queue", "/runs", "/runs/abc", "/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		api.routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s returned %d, expected a redirect to the login page", path, recorder.Code)
		}
		if location := recorder.Header().Get("Location"); location != "/login" {
			t.Fatalf("%s redirected to %q", path, location)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("the login page assets must stay reachable, got %d", recorder.Code)
	}
}

// TestCSRFRequiredForBrowserStateChanges covers the token and the origin check.
func TestCSRFRequiredForBrowserStateChanges(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	// No CSRF header at all.
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/android-build/runs?project=alpha", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	requireStatus(t, recorder, http.StatusForbidden)

	// Wrong CSRF token.
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/android-build/runs?project=alpha", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	req.Header.Set(csrfHeaderName, "not-the-token")
	recorder = httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	requireStatus(t, recorder, http.StatusForbidden)

	// Correct token but a cross-site origin.
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/android-build/runs?project=alpha", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	req.Header.Set(csrfHeaderName, session.CSRF)
	req.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	requireStatus(t, recorder, http.StatusForbidden)

	// Correct token and same origin.
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/android-build/runs?project=alpha", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	req.Header.Set(csrfHeaderName, session.CSRF)
	req.Header.Set("Origin", "http://"+req.Host)
	recorder = httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	requireStatus(t, recorder, http.StatusOK)

	// Reads never need CSRF.
	recorder = request(t, api, &Session{ID: session.ID}, http.MethodGet, "/api/jobs", nil)
	requireStatus(t, recorder, http.StatusOK)
}

// TestAPITokenSkipsCSRFButKeepsAdminScope covers the automation path.
func TestAPITokenSkipsCSRFButKeepsAdminScope(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	secret, _, err := controller.auth.IssueAPIToken("ci")
	if err != nil {
		t.Fatal(err)
	}
	recorder := tokenRequest(t, api, secret, http.MethodPost, "/api/jobs/android-build/runs?project=alpha", nil)
	requireStatus(t, recorder, http.StatusOK)

	recorder = tokenRequest(t, api, secret, http.MethodGet, "/api/runs", nil)
	requireStatus(t, recorder, http.StatusOK)

	recorder = tokenRequest(t, api, "not-a-token", http.MethodGet, "/api/runs", nil)
	requireStatus(t, recorder, http.StatusUnauthorized)
}

// TestAgentTokenScopeIsLimitedToItsOwnAgentAPI is the role separation test.
func TestAgentTokenScopeIsLimitedToItsOwnAgentAPI(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	secret, _, err := controller.auth.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}

	// An agent token cannot touch any admin surface.
	for _, endpoint := range [][2]string{
		{http.MethodGet, "/api/jobs"},
		{http.MethodGet, "/api/runs"},
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api/tokens"},
		{http.MethodPost, "/api/jobs/android-build/runs"},
		{http.MethodGet, "/api/queue"},
	} {
		recorder := tokenRequest(t, api, secret, endpoint[0], endpoint[1], nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("agent token reached %s %s with status %d", endpoint[0], endpoint[1], recorder.Code)
		}
	}

	// It can use the agent API for its own identity.
	recorder := tokenRequest(t, api, secret, http.MethodPost, "/api/agent/v1/heartbeat", AgentHeartbeatRequest{AgentID: "linux-one"})
	requireStatus(t, recorder, http.StatusOK)

	// But never for another identity.
	recorder = tokenRequest(t, api, secret, http.MethodPost, "/api/agent/v1/heartbeat", AgentHeartbeatRequest{AgentID: "linux-two"})
	requireStatus(t, recorder, http.StatusForbidden)
}

// TestAgentTokenRejectedAfterDeconfiguration proves a revoked agent cannot
// keep working from an old token.
func TestAgentTokenRejectedAfterDeconfiguration(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	secret, _, err := controller.auth.IssueAgentToken("linux-two")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.editConfig(func(cfg *ControllerConfig) error {
		kept := cfg.Agents[:0]
		for _, agent := range cfg.Agents {
			if agent.ID != "linux-two" {
				kept = append(kept, agent)
			}
		}
		cfg.Agents = kept
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recorder := tokenRequest(t, api, secret, http.MethodPost, "/api/agent/v1/heartbeat", AgentHeartbeatRequest{AgentID: "linux-two"})
	requireStatus(t, recorder, http.StatusForbidden)
}

// TestNoSecretHashesInResponses walks the admin read surfaces and asserts no
// verifier material is ever returned.
func TestNoSecretHashesInResponses(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	if err := controller.auth.SetAdminPassword(adminUserName, "a long enough password"); err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := controller.auth.IssueAPIToken("ci")
	if err != nil {
		t.Fatal(err)
	}
	agentSecret, _, err := controller.auth.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}
	session := adminSession(t, api)

	for _, path := range []string{"/api/meta", "/api/jobs", "/api/catalogs", "/api/agents", "/api/queue", "/api/runs", "/api/config", "/api/tokens"} {
		recorder := request(t, api, session, http.MethodGet, path, nil)
		requireStatus(t, recorder, http.StatusOK)
		body := recorder.Body.String()
		for _, forbidden := range []string{apiSecret, agentSecret, "\"hash\"", "\"salt\"", controller.auth.data.Admin.Hash, controller.auth.data.Admin.Salt} {
			if forbidden != "" && strings.Contains(body, forbidden) {
				t.Fatalf("%s leaked secret material %q", path, forbidden)
			}
		}
	}
}

// TestAgentTokenIssuedOnceThroughTheAPI covers the enroll path and the
// one-time disclosure.
func TestAgentTokenIssuedOnceThroughTheAPI(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)

	recorder := request(t, api, session, http.MethodPost, "/api/agents/linux-one/token", nil)
	requireStatus(t, recorder, http.StatusOK)
	var issued map[string]any
	decodeBody(t, recorder, &issued)
	secret, _ := issued["token"].(string)
	if secret == "" {
		t.Fatal("expected the raw token in the issue response")
	}
	if _, ok := controller.auth.VerifyAgentToken(secret); !ok {
		t.Fatal("the issued token must authenticate the agent")
	}

	// Listing never repeats the secret.
	recorder = request(t, api, session, http.MethodGet, "/api/tokens", nil)
	requireStatus(t, recorder, http.StatusOK)
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatal("the token list must not repeat the secret")
	}

	// Rotation invalidates the old one.
	recorder = request(t, api, session, http.MethodPost, "/api/agents/linux-one/token", nil)
	requireStatus(t, recorder, http.StatusOK)
	if _, ok := controller.auth.VerifyAgentToken(secret); ok {
		t.Fatal("rotation must invalidate the previous token")
	}

	// Revocation reports a clear not-found afterwards.
	recorder = request(t, api, session, http.MethodDelete, "/api/agents/linux-one/token", nil)
	requireStatus(t, recorder, http.StatusOK)
	recorder = request(t, api, session, http.MethodDelete, "/api/agents/linux-one/token", nil)
	requireStatus(t, recorder, http.StatusNotFound)

	// An unknown agent cannot be enrolled.
	recorder = request(t, api, session, http.MethodPost, "/api/agents/ghost/token", nil)
	requireStatus(t, recorder, http.StatusNotFound)
}

func TestLogoutClearsTheSession(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	recorder := request(t, api, session, http.MethodPost, "/api/logout", nil)
	requireStatus(t, recorder, http.StatusOK)
	recorder = request(t, api, session, http.MethodGet, "/api/meta", nil)
	requireStatus(t, recorder, http.StatusUnauthorized)
}

// TestPublicAssetMatchingRejectsTraversal keeps the anonymous asset prefix
// from becoming a way to fetch an application page without a session.
func TestPublicAssetMatchingRejectsTraversal(t *testing.T) {
	allowed := []string{"_astro/jobs.js", "_astro/index.css", "favicon.svg"}
	for _, path := range allowed {
		if !isPublicAsset(path) {
			t.Fatalf("%q should be publicly readable", path)
		}
	}
	denied := []string{
		"_astro/../index.html",
		"_astro/./../runs/index.html",
		"_astro//../index.html",
		"index.html",
		"runs/index.html",
		"settings/index.html",
		"../favicon.svg",
		"",
	}
	for _, path := range denied {
		if isPublicAsset(path) {
			t.Fatalf("%q must not be publicly readable", path)
		}
	}
}

// TestAnonymousCannotReachApplicationPagesByTraversal is the end-to-end check
// for the same concern.
func TestAnonymousCannotReachApplicationPagesByTraversal(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	for _, target := range []string{
		"/_astro/../index.html",
		"/_astro/%2e%2e/index.html",
		"/_astro/../settings/index.html",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		api.routes().ServeHTTP(recorder, req)
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s served a page to an anonymous caller: %s", target, recorder.Body.String()[:120])
		}
	}
}
