package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// servePage fetches one UI route as an authenticated browser.
func servePage(t *testing.T, api *ControllerAPI, session *Session, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

// TestControllerPagesAreServedForEveryRoute proves every management route maps
// to a prebuilt document in the embedded dist.
func TestControllerPagesAreServedForEveryRoute(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	markOnline(controller, "mac-one")
	execution := enqueue(t, controller, "ios-build", map[string]string{"project": "alpha"})

	cases := map[string][]string{
		"/":                     {`id="jobs"`, `id="job-editor"`, `id="run-modal"`, "/_astro/jobs.js"},
		"/jobs":                 {`id="jobs"`},
		"/catalogs":             {`id="catalogs"`, `id="catalog-options"`},
		"/agents":               {`id="agents"`, `id="agent-secret"`},
		"/queue":                {`id="queue"`, `id="cancel-selected"`, `id="active"`},
		"/runs":                 {`id="run-filters"`, `id="runs"`, `id="log"`},
		"/runs/" + execution.ID: {`id="badge-host"`, `id="rerun-run"`, `id="log"`},
		"/settings":             {`id="config-editor"`, `id="token-form"`},
	}
	for path, wanted := range cases {
		body := servePage(t, api, session, path)
		for _, needle := range wanted {
			if !strings.Contains(body, needle) {
				t.Fatalf("%s should contain %q", path, needle)
			}
		}
	}
	// An unknown route is still a 404 for an authenticated user.
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected an unknown route to 404, got %d", recorder.Code)
	}
}

// TestLoginPageIsReachableWithoutASession keeps the sign-in page and its
// assets outside the authenticated surface.
func TestLoginPageIsReachableWithoutASession(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	for _, path := range []string{"/login", "/favicon.svg"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		api.routes().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s returned %d for an anonymous caller", path, recorder.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	body := recorder.Body.String()
	for _, needle := range []string{`id="login-form"`, `name="password"`, "/_astro/login.js"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("the login page should contain %q", needle)
		}
	}
	if strings.Contains(body, "data-logout") {
		t.Fatal("the login page must not render the authenticated chrome")
	}
}

// TestEmbeddedUICoversTheControllerAPI pins the behaviour the shipped bundle
// has to keep: it talks to the controller API, carries the CSRF header, keeps
// the insecure-context copy fallback, and only repaints a log when the text
// changes so a completed log stays selectable.
func TestEmbeddedUICoversTheControllerAPI(t *testing.T) {
	for _, needle := range []string{
		"/api/jobs",
		"/api/catalogs",
		"/api/agents",
		"/api/queue",
		"/api/runs",
		"/api/tokens",
		"/api/login",
		"/api/logout",
		"X-Builda-CSRF",
		"X-Builda-Log-Offset",
		"document.execCommand",
		"window.isSecureContext",
		"BUILDA_PARAM_",
		"param-chip",
		"log-param-line",
		"data-theme-toggle",
		"data-locale-toggle",
		"data-build-id",
	} {
		assertEmbeddedWebContains(t, needle)
	}
}

// TestEmbeddedUIDropsTheStandaloneSurface makes sure the controller UI never
// ships calls to the removed standalone task API.
func TestEmbeddedUIDropsTheStandaloneSurface(t *testing.T) {
	for _, needle := range []string{"/api/tasks/", "/api/state?task=", "BUILDA_INPUT_"} {
		assertEmbeddedWebLacks(t, needle)
	}
}
