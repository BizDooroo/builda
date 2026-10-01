package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestAuthStore(t *testing.T) *AuthStore {
	t.Helper()
	store, err := newAuthStore(t.TempDir() + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAdminCredentialIsSaltedAndFileIsProtected(t *testing.T) {
	store := newTestAuthStore(t)
	if store.HasAdmin() {
		t.Fatal("a fresh controller must have no admin credential")
	}
	if err := store.VerifyPassword(adminUserName, "anything"); !errors.Is(err, errNoAdminCredential) {
		t.Fatalf("expected the bootstrap error, got %v", err)
	}
	if err := store.SetAdminPassword(adminUserName, "short"); err == nil {
		t.Fatal("expected a short password to be rejected")
	}
	if err := store.SetAdminPassword(adminUserName, "correct horse battery"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the credential file must be 0600, got %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "correct horse battery") {
		t.Fatal("the password must never be stored in clear text")
	}

	if err := store.VerifyPassword(adminUserName, "correct horse battery"); err != nil {
		t.Fatalf("the correct password must verify: %v", err)
	}
	if err := store.VerifyPassword(adminUserName, "wrong"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
	if err := store.VerifyPassword("someone-else", "correct horse battery"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("the user name must be checked, got %v", err)
	}
}

// TestPasswordSaltsDiffer proves two identical passwords do not share a hash.
func TestPasswordSaltsDiffer(t *testing.T) {
	first := newTestAuthStore(t)
	second := newTestAuthStore(t)
	if err := first.SetAdminPassword(adminUserName, "same password here"); err != nil {
		t.Fatal(err)
	}
	if err := second.SetAdminPassword(adminUserName, "same password here"); err != nil {
		t.Fatal(err)
	}
	if first.data.Admin.Salt == second.data.Admin.Salt {
		t.Fatal("each credential must get its own salt")
	}
	if first.data.Admin.Hash == second.data.Admin.Hash {
		t.Fatal("identical passwords must not share a verifier")
	}
	if first.data.Admin.Iterations != passwordIterations {
		t.Fatalf("unexpected iteration count %d", first.data.Admin.Iterations)
	}
}

func TestTokensReturnTheSecretOnceAndStoreOnlyAVerifier(t *testing.T) {
	store := newTestAuthStore(t)
	secret, record, err := store.IssueAPIToken("ci")
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || record.Hash == "" {
		t.Fatal("expected a secret and a stored verifier")
	}
	if strings.Contains(record.Hash, secret) {
		t.Fatal("the verifier must not contain the secret")
	}
	for _, listed := range store.APITokens() {
		if listed.Hash != "" {
			t.Fatal("listed tokens must never carry a verifier")
		}
	}
	if _, ok := store.VerifyAPIToken(secret); !ok {
		t.Fatal("the issued token must verify")
	}
	if _, ok := store.VerifyAPIToken(secret + "x"); ok {
		t.Fatal("a modified token must not verify")
	}
	if err := store.RevokeAPIToken(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.VerifyAPIToken(secret); ok {
		t.Fatal("a revoked token must stop working")
	}
	if err := store.RevokeAPIToken(record.ID); !errors.Is(err, errTokenNotFound) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestAgentTokenRotationReplacesThePreviousToken(t *testing.T) {
	store := newTestAuthStore(t)
	if _, _, err := store.IssueAgentToken("bad id"); err == nil {
		t.Fatal("expected an invalid agent id to be rejected")
	}
	first, _, err := store.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.VerifyAgentToken(first); ok {
		t.Fatal("rotation must invalidate the previous token")
	}
	record, ok := store.VerifyAgentToken(second)
	if !ok || record.AgentID != "linux-one" {
		t.Fatalf("the rotated token must map to the agent identity, got %+v", record)
	}
	if !store.HasAgentToken("linux-one") || store.HasAgentToken("mac-one") {
		t.Fatal("enrollment state is reported per agent")
	}
	if err := store.RevokeAgentToken("linux-one"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.VerifyAgentToken(second); ok {
		t.Fatal("a revoked agent token must stop working")
	}
}

func TestCredentialsSurviveReload(t *testing.T) {
	path := t.TempDir() + "/credentials.json"
	store, err := newAuthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminPassword(adminUserName, "a long enough password"); err != nil {
		t.Fatal(err)
	}
	secret, _, err := store.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := newAuthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.HasAdmin() {
		t.Fatal("the admin credential must survive a restart")
	}
	if _, ok := reloaded.VerifyAgentToken(secret); !ok {
		t.Fatal("agent tokens must survive a restart")
	}
}

func TestLoginRateLimit(t *testing.T) {
	limiter := newLoginLimiter(time.Minute, 3)
	if !limiter.Allowed("1.2.3.4") {
		t.Fatal("the first attempt must be allowed")
	}
	limiter.Fail("1.2.3.4")
	limiter.Fail("1.2.3.4")
	if !limiter.Allowed("1.2.3.4") {
		t.Fatal("two failures must still be under the limit")
	}
	if !limiter.Fail("1.2.3.4") {
		t.Fatal("the third failure must report the key as blocked")
	}
	if limiter.Allowed("1.2.3.4") {
		t.Fatal("a blocked key must be refused")
	}
	if !limiter.Allowed("5.6.7.8") {
		t.Fatal("the limit must be per client")
	}
	limiter.Reset("1.2.3.4")
	if !limiter.Allowed("1.2.3.4") {
		t.Fatal("a successful login must clear the history")
	}

	// The window expires.
	expiring := newLoginLimiter(20*time.Millisecond, 1)
	expiring.Fail("1.2.3.4")
	if expiring.Allowed("1.2.3.4") {
		t.Fatal("the failure must count inside the window")
	}
	time.Sleep(40 * time.Millisecond)
	if !expiring.Allowed("1.2.3.4") {
		t.Fatal("the failure must expire with the window")
	}
}

func TestSessionLifecycle(t *testing.T) {
	store := newSessionStore(50 * time.Millisecond)
	session, err := store.Create(adminUserName)
	if err != nil {
		t.Fatal(err)
	}
	if session.CSRF == "" || session.ID == "" || session.CSRF == session.ID {
		t.Fatalf("unexpected session %+v", session)
	}
	if _, ok := store.Get(session.ID); !ok {
		t.Fatal("the session must resolve")
	}
	if _, ok := store.Get(""); ok {
		t.Fatal("an empty id must not resolve")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := store.Get(session.ID); ok {
		t.Fatal("an expired session must not resolve")
	}

	live, _ := store.Create(adminUserName)
	store.Delete(live.ID)
	if _, ok := store.Get(live.ID); ok {
		t.Fatal("a deleted session must not resolve")
	}
	again, _ := store.Create(adminUserName)
	store.DeleteAll()
	if _, ok := store.Get(again.ID); ok {
		t.Fatal("DeleteAll must invalidate every session")
	}
}

// TestLoginFlowSetsHardenedCookie exercises the real login handler.
func TestLoginFlowSetsHardenedCookie(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	if err := controller.auth.SetAdminPassword(adminUserName, "a long enough password"); err != nil {
		t.Fatal(err)
	}
	recorder := postLogin(t, api, adminUserName, "a long enough password", false)
	requireStatus(t, recorder, http.StatusOK)

	cookie := findCookie(t, recorder, sessionCookieName)
	if !cookie.HttpOnly {
		t.Fatal("the session cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("expected a SameSite cookie, got %v", cookie.SameSite)
	}
	if cookie.Secure {
		t.Fatal("a plain HTTP request must not set a Secure cookie that the browser would drop")
	}

	var payload map[string]any
	decodeBody(t, recorder, &payload)
	if payload["csrf"] == "" || payload["csrf"] == nil {
		t.Fatal("login must return the CSRF token for the page to echo")
	}

	// Over HTTPS the same handler marks the cookie Secure.
	secure := postLogin(t, api, adminUserName, "a long enough password", true)
	requireStatus(t, secure, http.StatusOK)
	if !findCookie(t, secure, sessionCookieName).Secure {
		t.Fatal("an HTTPS login must set a Secure cookie")
	}
}

func TestLoginRejectsBadCredentialsAndThrottles(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	if err := controller.auth.SetAdminPassword(adminUserName, "a long enough password"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < loginFailureLimit; i++ {
		recorder := postLogin(t, api, adminUserName, "wrong", false)
		if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d returned %d: %s", i, recorder.Code, recorder.Body.String())
		}
	}
	// Once throttled, even the correct password is refused.
	recorder := postLogin(t, api, adminUserName, "a long enough password", false)
	requireStatus(t, recorder, http.StatusTooManyRequests)
}

func TestLoginBeforeBootstrapIsRefused(t *testing.T) {
	api, _ := newTestAPI(t, testControllerConfig)
	recorder := postLogin(t, api, adminUserName, "whatever long enough", false)
	requireStatus(t, recorder, http.StatusServiceUnavailable)
	if !strings.Contains(recorder.Body.String(), "admin set-password") {
		t.Fatalf("the error must point at the local bootstrap command, got %s", recorder.Body.String())
	}
}

func postLogin(t *testing.T, api *ControllerAPI, user, password string, overTLS bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":"` + user + `","password":"` + password + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if overTLS {
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	recorder := httptest.NewRecorder()
	api.routes().ServeHTTP(recorder, req)
	return recorder
}

func findCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q was not set", name)
	return nil
}
