package main

import (
	"strings"
	"testing"
)

func parseFixture(t *testing.T, document string) ControllerConfig {
	t.Helper()
	cfg, err := parseControllerConfig([]byte(document))
	if err != nil {
		t.Fatalf("parse controller config: %v", err)
	}
	return cfg
}

func expectConfigError(t *testing.T, document, needle string) {
	t.Helper()
	_, err := parseControllerConfig([]byte(document))
	if err == nil {
		t.Fatalf("expected a validation error mentioning %q", needle)
	}
	if !strings.Contains(err.Error(), needle) {
		t.Fatalf("expected error to mention %q, got %v", needle, err)
	}
}

func TestSampleControllerConfigIsValid(t *testing.T) {
	cfg := parseFixture(t, sampleControllerConfig)
	if len(cfg.Jobs) != 2 || len(cfg.Catalogs) != 1 || len(cfg.Agents) != 2 {
		t.Fatalf("unexpected sample shape: %d jobs, %d catalogs, %d agents", len(cfg.Jobs), len(cfg.Catalogs), len(cfg.Agents))
	}
	android, ok := findJob(cfg, "android-build")
	if !ok {
		t.Fatal("expected an android-build job")
	}
	if android.Timeout != "45m" || strings.Join(android.Labels, ",") != "linux,android" {
		t.Fatalf("unexpected android job: %+v", android)
	}
	ios, ok := findJob(cfg, "ios-build")
	if !ok {
		t.Fatal("expected an ios-build job")
	}
	if ios.Timeout != "2h" || strings.Join(ios.Labels, ",") != "macos,ios" {
		t.Fatalf("unexpected ios job: %+v", ios)
	}
	if !android.IsEnabled() {
		t.Fatal("jobs must default to enabled")
	}
	// The sample ships no credentials of any kind.
	for _, needle := range []string{"password", "token", "secret"} {
		if strings.Contains(strings.ToLower(sampleControllerConfig), needle+":") {
			t.Fatalf("sample controller config must not contain a %s field", needle)
		}
	}
}

func TestSampleAgentConfigIsValid(t *testing.T) {
	cfg, err := parseAgentConfig([]byte(sampleAgentConfig))
	if err != nil {
		t.Fatalf("parse agent config: %v", err)
	}
	if cfg.Agent.ID == "" || cfg.Agent.WorkspaceRoot == "" || cfg.Agent.ControllerURL == "" {
		t.Fatalf("unexpected agent config: %+v", cfg.Agent)
	}
	if !strings.Contains(cfg.Agent.ScriptHeader, "#!/usr/bin/env bash") {
		t.Fatal("agent sample must carry a shell header")
	}
	if strings.Contains(strings.ToLower(sampleAgentConfig), "token:") {
		t.Fatal("agent sample config must not contain a token")
	}
}

func TestRoleMismatchIsRejected(t *testing.T) {
	if err := requireConfigRole("p.yaml", []byte(sampleAgentConfig), RoleController); err == nil {
		t.Fatal("expected an agent config to be rejected as a controller config")
	}
	if err := requireConfigRole("p.yaml", []byte(sampleConfig), RoleController); err == nil {
		t.Fatal("expected a legacy config to be rejected as a controller config")
	} else if !strings.Contains(err.Error(), "legacy standalone") {
		t.Fatalf("expected a legacy-specific message, got %v", err)
	}
	if _, err := detectConfigRole([]byte("role: nonsense\n")); err == nil {
		t.Fatal("expected an unknown role to be rejected")
	}
}

func TestCatalogValidation(t *testing.T) {
	expectConfigError(t, "role: controller\ncatalogs:\n  - id: \"bad id\"\n    options:\n      - value: a\n", "catalogs[0].id")
	expectConfigError(t, "role: controller\ncatalogs:\n  - id: a\n", "requires at least one option")
	expectConfigError(t, "role: controller\ncatalogs:\n  - id: a\n    options:\n      - value: x\n      - value: x\n", "duplicate option value")
	expectConfigError(t, "role: controller\ncatalogs:\n  - id: a\n    options:\n      - value: x\n  - id: a\n    options:\n      - value: y\n", "duplicate catalog id")
	expectConfigError(t, "role: controller\ncatalogs:\n  - id: a\n    options:\n      - value: x\n        values:\n          \"bad key\": y\n", "values key")

	cfg := parseFixture(t, "role: controller\ncatalogs:\n  - id: a\n    options:\n      - value: x\n        labels: [\"Android\", \"android\"]\n")
	option := cfg.Catalogs[0].Options[0]
	if option.Label != "x" {
		t.Fatalf("option label should default to the value, got %q", option.Label)
	}
	if strings.Join(option.Labels, ",") != "android" {
		t.Fatalf("labels must be lowercased and de-duplicated, got %v", option.Labels)
	}
}

// TestCatalogPathValidation keeps traversal and absolute paths out of config.
func TestCatalogPathValidation(t *testing.T) {
	for _, bad := range []string{"/abs/path", "../escape", "a/../b", "./a", "a//b", "~/home", "a/", " lead", `a\\b`} {
		document := "role: controller\ncatalogs:\n  - id: a\n    options:\n      - value: x\n        values:\n          path: '" + bad + "'\n"
		expectConfigError(t, document, "values.path is invalid")
	}
	cfg := parseFixture(t, "role: controller\ncatalogs:\n  - id: a\n    options:\n      - value: x\n        values:\n          path: \"nested/deep/project\"\n")
	if cfg.Catalogs[0].Options[0].Values[optionPathField] != "nested/deep/project" {
		t.Fatal("a nested relative path must be accepted")
	}
}

func TestJobValidation(t *testing.T) {
	expectConfigError(t, "role: controller\njobs:\n  - id: a\n", "requires a script")
	expectConfigError(t, "role: controller\njobs:\n  - id: a\n    script: x\n    timeout: \"nope\"\n", "timeout is invalid")
	expectConfigError(t, "role: controller\njobs:\n  - id: a\n    script: x\n    timeout: \"0s\"\n", "timeout must be greater than zero")
	expectConfigError(t, "role: controller\njobs:\n  - id: a\n    script: x\n  - id: a\n    script: y\n", "duplicate job id")
	expectConfigError(t, "role: controller\njobs:\n  - id: a\n    script: x\n    labels: [\"bad label\"]\n", "labels are invalid")

	cfg := parseFixture(t, "role: controller\njobs:\n  - id: a\n    script: x\n")
	if cfg.Jobs[0].Name != "a" {
		t.Fatalf("job name should default to the id, got %q", cfg.Jobs[0].Name)
	}
}

func TestParameterSourceValidation(t *testing.T) {
	base := "role: controller\ncatalogs:\n  - id: projects\n    options:\n      - value: alpha\n        labels: [android]\njobs:\n  - id: a\n    script: x\n    parameters:\n"
	expectConfigError(t, base+"      - id: p\n        type: choice\n", "requires inline options or a catalog")
	expectConfigError(t, base+"      - id: p\n        type: choice\n        catalog: projects\n        options:\n          - value: y\n", "not both")
	expectConfigError(t, base+"      - id: p\n        type: choice\n        catalog: missing\n", "unknown catalog")
	expectConfigError(t, base+"      - id: p\n        type: choice\n        catalog: projects\n        catalog_labels: [ios]\n", "selects no options")
	expectConfigError(t, base+"      - id: p\n        type: string\n        options:\n          - value: y\n", "declares options but is not a choice")
	expectConfigError(t, base+"      - id: p\n        type: string\n        catalog: projects\n", "declares a catalog but is not a choice")
	expectConfigError(t, base+"      - id: p\n        type: choice\n        options:\n          - value: y\n        catalog_labels: [android]\n", "catalog_labels without a catalog")
	expectConfigError(t, base+"      - id: p\n        type: bogus\n", "type is invalid")
	expectConfigError(t, base+"      - id: p\n        type: choice\n        options:\n          - value: y\n        default: z\n", "is not a selectable option")
	expectConfigError(t, base+"      - id: p\n        type: boolean\n        default: maybe\n", "default is invalid")
	expectConfigError(t, base+"      - id: p\n        type: string\n      - id: p\n        type: string\n", "duplicate parameter id")

	cfg := parseFixture(t, base+"      - id: p\n        type: choice\n        catalog: projects\n        catalog_labels: [android]\n")
	if cfg.Jobs[0].Parameters[0].Name != "p" {
		t.Fatal("parameter name should default to the id")
	}
	if cfg.Jobs[0].Parameters[0].Type != paramTypeChoice {
		t.Fatal("expected a choice parameter")
	}
}

// TestParameterEnvCollisionsRejected covers the normalized-name collisions a
// parameter set can produce, including through option metadata fields.
func TestParameterEnvCollisionsRejected(t *testing.T) {
	// "ad-hoc" and "ad_hoc" normalize to the same variable name.
	expectConfigError(t,
		"role: controller\njobs:\n  - id: a\n    script: x\n    parameters:\n      - id: ad-hoc\n        type: string\n      - id: ad_hoc\n        type: string\n",
		"already produced by")
	// A metadata field of one parameter can collide with another parameter.
	expectConfigError(t,
		"role: controller\njobs:\n  - id: a\n    script: x\n    parameters:\n      - id: project\n        type: choice\n        options:\n          - value: v\n            values:\n              path: p\n      - id: project_path\n        type: string\n",
		"already produced by")
}

// TestReservedEnvNamesCannotBeShadowed covers the two guards that keep
// execution identity trustworthy: config validation refuses a declaration
// whose variable name is reserved, and the agent ignores any assignment entry
// that targets a reserved name.
func TestReservedEnvNamesCannotBeShadowed(t *testing.T) {
	checker := newEnvCollisionChecker()
	if err := checker.add("BUILDA_WORKSPACE", "jobs[0] parameters[0]"); err == nil {
		t.Fatal("expected a reserved variable name to be rejected")
	} else if !strings.Contains(err.Error(), "reserved environment variable") {
		t.Fatalf("unexpected error %v", err)
	}
	if err := checker.add("BUILDA_PARAM_A", "first"); err != nil {
		t.Fatal(err)
	}
	if err := checker.add("BUILDA_PARAM_A", "second"); err == nil {
		t.Fatal("expected a duplicate variable name to be rejected")
	}

	runtime := AgentRuntime{ID: "real-agent", WorkspaceRoot: "/w", ControllerURL: "http://c"}
	assignment := AgentAssignment{
		ExecutionID: "e1",
		JobID:       "j1",
		Env: map[string]string{
			"BUILDA_AGENT_ID":     "spoofed",
			"BUILDA_WORKSPACE":    "/etc",
			"BUILDA_PARAM_ACTION": "debug",
		},
	}
	env := environMap(buildExecutionEnv(runtime, assignment))
	if env["BUILDA_AGENT_ID"] != "real-agent" {
		t.Fatalf("agent identity must not be overridable, got %q", env["BUILDA_AGENT_ID"])
	}
	if env["BUILDA_WORKSPACE"] != "/w" {
		t.Fatalf("workspace must not be overridable, got %q", env["BUILDA_WORKSPACE"])
	}
	if env["BUILDA_PARAM_ACTION"] != "debug" {
		t.Fatalf("declared parameters must pass through, got %q", env["BUILDA_PARAM_ACTION"])
	}
}

func TestParameterEnvNamesCoverCatalogFields(t *testing.T) {
	cfg := parseFixture(t, testControllerConfig)
	job, _ := findJob(cfg, "android-build")
	names := paramEnvNames(job.Parameters[0], parameterOptions(cfg, job.Parameters[0]))
	want := map[string]bool{
		"BUILDA_PARAM_PROJECT":       false,
		"BUILDA_PARAM_PROJECT_LABEL": false,
		"BUILDA_PARAM_PROJECT_PATH":  false,
	}
	for _, name := range names {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("expected parameter env names to include %s, got %v", name, names)
		}
	}
}

func TestWorkdirParamMustProvideAPath(t *testing.T) {
	expectConfigError(t,
		"role: controller\njobs:\n  - id: a\n    script: x\n    workdir_param: action\n    parameters:\n      - id: action\n        type: choice\n        options:\n          - value: debug\n",
		"must name a parameter whose options declare a values.path entry")
	expectConfigError(t,
		"role: controller\njobs:\n  - id: a\n    script: x\n    workdir_param: missing\n",
		"workdir_param")
}

// TestCatalogLabelFilterRequiresAllLabels pins the all-label filter semantics.
func TestCatalogLabelFilterRequiresAllLabels(t *testing.T) {
	options := []OptionConfig{
		{Value: "both", Labels: []string{"android", "ios"}},
		{Value: "android-only", Labels: []string{"android"}},
		{Value: "none"},
	}
	if got := filterCatalogOptions(options, []string{"android"}); len(got) != 2 {
		t.Fatalf("android filter should keep 2 options, got %d", len(got))
	}
	if got := filterCatalogOptions(options, []string{"android", "ios"}); len(got) != 1 || got[0].Value != "both" {
		t.Fatalf("all-label filter should keep only the option carrying both labels, got %v", got)
	}
	if got := filterCatalogOptions(options, nil); len(got) != 3 {
		t.Fatalf("an empty filter should keep every option, got %d", len(got))
	}
}

func TestControllerServerValidation(t *testing.T) {
	expectConfigError(t, "role: controller\nserver:\n  max_history: -1\n", "max_history")
	expectConfigError(t, "role: controller\nserver:\n  offline_after: \"nope\"\n", "offline_after")
	expectConfigError(t, "role: controller\nserver:\n  heartbeat_interval: \"0s\"\n", "heartbeat_interval must be greater than zero")

	cfg := parseFixture(t, "role: controller\n")
	runtime, err := controllerRuntime("/tmp/x/controller.yaml", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.MaxHistory != defaultMaxHistory {
		t.Fatalf("expected the default history cap, got %d", runtime.MaxHistory)
	}
	if runtime.HeartbeatInterval != defaultHeartbeatInterval || runtime.OfflineAfter != defaultOfflineAfter || runtime.LongPollTimeout != defaultLongPollTimeout {
		t.Fatalf("unexpected runtime timings: %+v", runtime)
	}
	if runtime.StateDir != "/tmp/x/state" || runtime.LogDir != "/tmp/x/state/logs" {
		t.Fatalf("state dir must resolve next to the config file, got %q", runtime.StateDir)
	}
	if runtime.ListenAddresses[0] != defaultControllerAddress {
		t.Fatalf("unexpected default listen address %v", runtime.ListenAddresses)
	}
}

func TestAgentConfigValidation(t *testing.T) {
	for _, document := range []string{
		"role: agent\nagent:\n  controller_url: http://x\n  workspace_root: /w\n",
		"role: agent\nagent:\n  id: a\n  workspace_root: /w\n",
		"role: agent\nagent:\n  id: a\n  controller_url: ftp://x\n  workspace_root: /w\n",
		"role: agent\nagent:\n  id: a\n  controller_url: http://x\n",
		"role: agent\nagent:\n  id: a\n  controller_url: http://x\n  workspace_root: relative\n",
		"role: agent\nagent:\n  id: a\n  controller_url: http://x\n  workspace_root: /w\n  poll_timeout: nope\n",
	} {
		if _, err := parseAgentConfig([]byte(document)); err == nil {
			t.Fatalf("expected %q to be rejected", document)
		}
	}
	cfg, err := parseAgentConfig([]byte("role: agent\nagent:\n  id: a\n  controller_url: \"http://x:1/\"\n  workspace_root: /w\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ControllerURL != "http://x:1" {
		t.Fatalf("controller url should lose its trailing slash, got %q", cfg.Agent.ControllerURL)
	}
	if cfg.Agent.ScriptHeader != defaultScriptHeader {
		t.Fatalf("expected a default script header, got %q", cfg.Agent.ScriptHeader)
	}
}
