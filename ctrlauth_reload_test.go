package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRevocationFromAnotherProcessTakesEffectImmediately is the regression
// guard for the highest-severity security finding: the credential file is
// written by the CLI from a separate process, so a running controller that
// trusted its in-memory copy kept honouring a revoked token until restart.
func TestRevocationFromAnotherProcessTakesEffectImmediately(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	secret, _, err := controller.auth.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}
	recorder := tokenRequest(t, api, secret, http.MethodPost, "/api/agent/v1/heartbeat",
		AgentHeartbeatRequest{AgentID: "linux-one"})
	requireStatus(t, recorder, http.StatusOK)

	// Another process, the CLI, revokes the token.
	cli, err := newAuthStore(controller.Runtime().CredentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.RevokeAgentToken("linux-one"); err != nil {
		t.Fatal(err)
	}

	recorder = tokenRequest(t, api, secret, http.MethodPost, "/api/agent/v1/heartbeat",
		AgentHeartbeatRequest{AgentID: "linux-one"})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked token must stop working at once, got %d", recorder.Code)
	}
}

// TestControllerWriteDoesNotResurrectRevokedCredentials covers the other half:
// a controller-side write that started from a stale in-memory document would
// put the revoked token back into the file.
func TestControllerWriteDoesNotResurrectRevokedCredentials(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	session := adminSession(t, api)
	leaked, _, err := controller.auth.IssueAPIToken("leaked")
	if err != nil {
		t.Fatal(err)
	}

	cli, err := newAuthStore(controller.Runtime().CredentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	tokens := cli.APITokens()
	if len(tokens) != 1 {
		t.Fatalf("expected one token, got %d", len(tokens))
	}
	if err := cli.RevokeAPIToken(tokens[0].ID); err != nil {
		t.Fatal(err)
	}

	// The controller issues an unrelated token, which rewrites the file.
	recorder := request(t, api, session, http.MethodPost, "/api/tokens", map[string]string{"name": "fresh"})
	requireStatus(t, recorder, http.StatusOK)

	if _, ok := controller.auth.VerifyAPIToken(leaked); ok {
		t.Fatal("a later controller write must not resurrect a revoked token")
	}
	data, err := os.ReadFile(controller.Runtime().CredentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), hashToken(leaked)) {
		t.Fatal("the revoked verifier must be gone from the file")
	}
	// The newly issued token still works, so the rewrite was not simply lost.
	var issued struct{ Token string }
	decodeBody(t, recorder, &issued)
	if _, ok := controller.auth.VerifyAPIToken(issued.Token); !ok {
		t.Fatal("the freshly issued token must work")
	}
}

// TestPasswordRotationFromTheCLIInvalidatesSessions proves a rotation made in
// another process ends existing browser sessions.
func TestPasswordRotationFromTheCLIInvalidatesSessions(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	if err := controller.auth.SetAdminPassword(adminUserName, "the first password"); err != nil {
		t.Fatal(err)
	}
	session := adminSession(t, api)
	requireStatus(t, request(t, api, session, http.MethodGet, "/api/meta", nil), http.StatusOK)

	time.Sleep(5 * time.Millisecond)
	cli, err := newAuthStore(controller.Runtime().CredentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.SetAdminPassword(adminUserName, "the second password"); err != nil {
		t.Fatal(err)
	}

	if recorder := request(t, api, session, http.MethodGet, "/api/meta", nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a session older than the rotated credential must be refused, got %d", recorder.Code)
	}
	// The old password no longer verifies and the new one does.
	if err := controller.auth.VerifyPassword(adminUserName, "the first password"); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("the old password must stop working, got %v", err)
	}
	if err := controller.auth.VerifyPassword(adminUserName, "the second password"); err != nil {
		t.Fatalf("the rotated password must work, got %v", err)
	}
	// A session created after the rotation is accepted.
	fresh := adminSession(t, api)
	requireStatus(t, request(t, api, fresh, http.MethodGet, "/api/meta", nil), http.StatusOK)
}

// TestCredentialFileSizeIsBounded keeps a corrupt or hostile file from being
// read into memory without limit.
func TestCredentialFileSizeIsBounded(t *testing.T) {
	path := t.TempDir() + "/credentials.json"
	store, err := newAuthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdminPassword(adminUserName, "a long enough password"); err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, maxCredentialFileBytes+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyPassword(adminUserName, "a long enough password"); err == nil {
		t.Fatal("an oversized credential file must not authenticate anyone")
	}
	if _, ok := store.VerifyAPIToken("anything"); ok {
		t.Fatal("an unreadable credential file must authenticate nobody")
	}
}

// TestAgentTokenCannotFetchManagementPages keeps the UI to browser sessions.
func TestAgentTokenCannotFetchManagementPages(t *testing.T) {
	api, controller := newTestAPI(t, testControllerConfig)
	secret, _, err := controller.auth.IssueAgentToken("linux-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/agents", "/settings"} {
		recorder := tokenRequest(t, api, secret, http.MethodGet, path, nil)
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("%s served a page to an agent token with status %d", path, recorder.Code)
		}
	}
}
