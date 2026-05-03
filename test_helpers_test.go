package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "builda-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", home)
	}
	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(home)
	}
	os.Exit(code)
}

func assertEmbeddedWebContains(t *testing.T, needle string) {
	t.Helper()
	dist, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	err = fs.WalkDir(dist, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return err
		}
		data, err := fs.ReadFile(dist, path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected embedded web assets to contain %q", needle)
	}
}

func waitForRun(t *testing.T, run *Run) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}

func newConfigSaveRequest(content, password string) *http.Request {
	body := url.Values{"content": {content}, "password": {password}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
