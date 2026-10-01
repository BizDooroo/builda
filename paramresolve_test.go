package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureConfig(t *testing.T) ControllerConfig {
	t.Helper()
	return parseFixture(t, testControllerConfig)
}

func resolve(t *testing.T, jobID string, values map[string]string) (ResolvedParameters, error) {
	t.Helper()
	cfg := fixtureConfig(t)
	job, ok := findJob(cfg, jobID)
	if !ok {
		t.Fatalf("job %s missing from fixture", jobID)
	}
	return resolveParameters(cfg, job, parameterValuesToQuery(values))
}

func TestResolveParametersBuildsEnvironmentAndMetadata(t *testing.T) {
	resolved, err := resolve(t, "android-build", map[string]string{"project": "beta"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Values["project"] != "beta" || resolved.Values["action"] != "debug" {
		t.Fatalf("unexpected values %v", resolved.Values)
	}
	want := map[string]string{
		"BUILDA_PARAM_PROJECT":       "beta",
		"BUILDA_PARAM_PROJECT_LABEL": "Beta",
		"BUILDA_PARAM_PROJECT_PATH":  "nested/beta",
		"BUILDA_PARAM_ACTION":        "debug",
	}
	for name, value := range want {
		if resolved.Env[name] != value {
			t.Fatalf("expected %s=%q, got %q", name, value, resolved.Env[name])
		}
	}
	if resolved.WorkdirPath != "nested/beta" {
		t.Fatalf("expected the workdir to follow the selected project, got %q", resolved.WorkdirPath)
	}
	if resolved.Options["project"].Value != "beta" {
		t.Fatal("expected the selected option to be snapshotted")
	}
}

func TestResolveParametersRejectsBadInput(t *testing.T) {
	if _, err := resolve(t, "android-build", map[string]string{"project": "alpha", "nope": "1"}); err == nil {
		t.Fatal("expected an undeclared parameter to be rejected")
	} else if !strings.Contains(err.Error(), "unknown parameter") {
		t.Fatalf("unexpected error %v", err)
	}
	if _, err := resolve(t, "android-build", nil); err == nil {
		t.Fatal("expected a missing required parameter to be rejected")
	}
	if _, err := resolve(t, "android-build", map[string]string{"project": "ghost"}); err == nil {
		t.Fatal("expected an invalid choice to be rejected")
	}
	// The iOS job filters the catalog to ios-labelled options, so an
	// android-only project is not selectable there.
	if _, err := resolve(t, "ios-build", map[string]string{"project": "beta"}); err == nil {
		t.Fatal("expected a label-filtered option to be rejected")
	}
	if _, err := resolve(t, "ios-build", map[string]string{"project": "alpha"}); err != nil {
		t.Fatalf("alpha carries the ios label and must be selectable: %v", err)
	}
}

func TestResolveParametersRejectsRepeatedValues(t *testing.T) {
	cfg := fixtureConfig(t)
	job, _ := findJob(cfg, "android-build")
	values := url.Values{}
	values.Add("project", "alpha")
	values.Add("project", "beta")
	if _, err := resolveParameters(cfg, job, values); err == nil {
		t.Fatal("expected a repeated parameter to be rejected")
	} else if !strings.Contains(err.Error(), "provide it once") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestResolveParametersIgnoresWaitControlParameter(t *testing.T) {
	cfg := fixtureConfig(t)
	job, _ := findJob(cfg, "android-build")
	values := url.Values{"project": {"alpha"}, runWaitParam: {"1"}}
	resolved, err := resolveParameters(cfg, job, values)
	if err != nil {
		t.Fatalf("wait must be accepted as a control parameter: %v", err)
	}
	if _, ok := resolved.Values[runWaitParam]; ok {
		t.Fatal("wait must never reach the script as a parameter")
	}
}

func TestResolveBooleanParameter(t *testing.T) {
	document := "role: controller\njobs:\n  - id: j\n    script: x\n    parameters:\n      - id: clean\n        type: boolean\n        default: \"no\"\n"
	cfg := parseFixture(t, document)
	job, _ := findJob(cfg, "j")
	if cfg.Jobs[0].Parameters[0].Default != "no" {
		t.Fatalf("the raw default should be preserved, got %q", cfg.Jobs[0].Parameters[0].Default)
	}
	resolved, err := resolveParameters(cfg, job, url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Env["BUILDA_PARAM_CLEAN"] != "false" {
		t.Fatalf("boolean defaults must normalize, got %q", resolved.Env["BUILDA_PARAM_CLEAN"])
	}
	resolved, err = resolveParameters(cfg, job, url.Values{"clean": {"YES"}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Env["BUILDA_PARAM_CLEAN"] != "true" {
		t.Fatalf("expected true, got %q", resolved.Env["BUILDA_PARAM_CLEAN"])
	}
	if _, err := resolveParameters(cfg, job, url.Values{"clean": {"perhaps"}}); err == nil {
		t.Fatal("expected an invalid boolean to be rejected")
	}
}

// TestParameterValuesAreNotShellInterpreted proves a hostile parameter value
// reaches the script as literal text instead of being substituted or evaluated.
func TestParameterValuesAreNotShellInterpreted(t *testing.T) {
	hostile := "$(touch /tmp/builda-should-not-exist); `id`; \"quoted\" 'single' \\ end"
	document := "role: controller\njobs:\n  - id: j\n    script: 'printf %s \"$BUILDA_PARAM_TEXT\"'\n    parameters:\n      - id: text\n        type: string\n"
	cfg := parseFixture(t, document)
	job, _ := findJob(cfg, "j")
	resolved, err := resolveParameters(cfg, job, url.Values{"text": {hostile}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Env["BUILDA_PARAM_TEXT"] != hostile {
		t.Fatalf("value must survive verbatim, got %q", resolved.Env["BUILDA_PARAM_TEXT"])
	}
	// The rendered script never embeds the value, so there is nothing for the
	// shell to expand.
	content := taskScriptContent(defaultScriptHeader, job.Script)
	if strings.Contains(content, hostile) {
		t.Fatal("the script body must never embed a parameter value")
	}
	env := environMap(buildExecutionEnv(AgentRuntime{ID: "a", WorkspaceRoot: "/w"}, AgentAssignment{Env: resolved.Env}))
	if env["BUILDA_PARAM_TEXT"] != hostile {
		t.Fatalf("the agent must pass the value through unchanged, got %q", env["BUILDA_PARAM_TEXT"])
	}
}

func TestSanitizeInheritedEnvDropsSpoofedValues(t *testing.T) {
	environ := []string{"PATH=/bin", "BUILDA_PARAM_PROJECT=evil", "BUILDA_AGENT_ID=evil", "HOME=/home/me"}
	kept := sanitizeInheritedEnv(environ)
	for _, entry := range kept {
		if strings.HasPrefix(entry, buildaEnvPrefix) {
			t.Fatalf("inherited %q must be dropped", entry)
		}
	}
	if len(kept) != 2 {
		t.Fatalf("expected the unrelated variables to survive, got %v", kept)
	}
}

func TestParameterValuesToQueryRoundTrip(t *testing.T) {
	query := parameterValuesToQuery(map[string]string{"project": "alpha", "action": "", "flag": "1"})
	if query.Get("project") != "alpha" || query.Get("flag") != "1" {
		t.Fatalf("unexpected query %v", query)
	}
	if query.Has("action") {
		t.Fatal("an empty value must be omitted so the job default applies on rerun")
	}
}

func TestResolveWorkspacePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveWorkspacePath(root, "nested/beta")
	if err != nil {
		t.Fatalf("a nested path must resolve: %v", err)
	}
	if !strings.HasSuffix(resolved, filepath.Join("nested", "beta")) {
		t.Fatalf("unexpected resolved path %q", resolved)
	}
	if _, err := resolveWorkspacePath(root, "escape"); err == nil {
		t.Fatal("a symlink leaving the workspace root must be rejected")
	}
	if _, err := resolveWorkspacePath(root, "../other"); err == nil {
		t.Fatal("traversal must be rejected")
	}
	if _, err := resolveWorkspacePath(root, "/etc"); err == nil {
		t.Fatal("an absolute path must be rejected")
	}
	if _, err := resolveWorkspacePath(root, "missing"); err == nil {
		t.Fatal("a path that does not exist must be rejected")
	}
	if _, err := resolveWorkspacePath("", "nested"); err == nil {
		t.Fatal("an empty workspace root must be rejected")
	}
}

// TestResolveWorkspacePathRejectsNestedSymlinkEscape covers a symlink hidden
// inside the path rather than at its end.
func TestResolveWorkspacePathRejectsNestedSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspacePath(root, "link/project"); err == nil {
		t.Fatal("a symlinked parent directory must be rejected")
	}
}
