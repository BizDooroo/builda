package main

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MigrationMapping is the reviewable plan that connects legacy tasks to new
// jobs, catalog entries, and parameters. "migrate plan" drafts it; an operator
// edits it; "migrate config" and "migrate import" consume it.
type MigrationMapping struct {
	Machine      string                 `yaml:"machine"`
	Agent        MappingAgent           `yaml:"agent"`
	CatalogID    string                 `yaml:"catalog_id"`
	ProjectParam string                 `yaml:"project_parameter"`
	Jobs         []MappingJob           `yaml:"jobs"`
	Tasks        map[string]MappingTask `yaml:"tasks"`
	Unmapped     []string               `yaml:"unmapped,omitempty"`
}

type MappingAgent struct {
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name,omitempty"`
	Labels        []string `yaml:"labels"`
	WorkspaceRoot string   `yaml:"workspace_root"`
}

type MappingJob struct {
	ID           string            `yaml:"id"`
	Name         string            `yaml:"name"`
	Labels       []string          `yaml:"labels"`
	Timeout      string            `yaml:"timeout"`
	WorkdirParam string            `yaml:"workdir_param,omitempty"`
	Script       string            `yaml:"script"`
	ActionParam  string            `yaml:"action_parameter,omitempty"`
	Actions      []string          `yaml:"actions,omitempty"`
	ActionLabels map[string]string `yaml:"action_labels,omitempty"`
}

type MappingTask struct {
	Job         string            `yaml:"job"`
	Project     string            `yaml:"project,omitempty"`
	ProjectPath string            `yaml:"project_path,omitempty"`
	Labels      []string          `yaml:"project_labels,omitempty"`
	Parameters  map[string]string `yaml:"parameters,omitempty"`
	InputMap    map[string]string `yaml:"input_map,omitempty"`
	InputValues map[string]string `yaml:"input_values,omitempty"`
}

// scriptProjectPattern extracts the workspace-relative project path and the
// platform from a legacy task script such as
//
//	bash /home/me/git/dooroo/focus_stopwatch/tools/build_android.sh "$BUILDA_INPUT_ACTION"
var scriptProjectPattern = regexp.MustCompile(`([A-Za-z0-9._/-]+)/tools/build_(android|ios)\.sh`)

// legacyActionAliases map the legacy iOS action names onto the new values.
var legacyActionAliases = map[string]string{
	"adhoc":  "ad-hoc",
	"deploy": "app-store",
}

// planMigration drafts a mapping from a bundle. Everything it infers is
// written to the mapping file so an operator can review and correct it before
// anything is applied.
func planMigration(bundle *MigrationBundle, agentID, workspaceRoot string) (*MigrationMapping, []string) {
	mapping := &MigrationMapping{
		Machine:      bundle.Machine,
		CatalogID:    "projects",
		ProjectParam: "project",
		Tasks:        map[string]MappingTask{},
	}
	diagnostics := make([]string, 0)

	platforms := map[string]bool{}
	roots := map[string]int{}
	actions := map[string]map[string]bool{}

	for _, task := range bundle.Tasks {
		match := scriptProjectPattern.FindStringSubmatch(task.Script)
		if match == nil {
			mapping.Unmapped = append(mapping.Unmapped, task.ID)
			diagnostics = append(diagnostics, fmt.Sprintf("task %q does not match a known build script shape; map it by hand", task.ID))
			continue
		}
		absProject, platform := match[1], match[2]
		root, project := path.Split(absProject)
		root = strings.TrimSuffix(root, "/")
		if root == "" || project == "" {
			mapping.Unmapped = append(mapping.Unmapped, task.ID)
			diagnostics = append(diagnostics, fmt.Sprintf("task %q has an unexpected project path %q", task.ID, absProject))
			continue
		}
		roots[root]++
		platforms[platform] = true

		jobID := platform + "-build"
		entry := MappingTask{
			Job:         jobID,
			Project:     project,
			ProjectPath: project,
			Labels:      []string{platform},
			Parameters:  map[string]string{mapping.ProjectParam: project},
			InputMap:    map[string]string{},
			InputValues: map[string]string{},
		}
		for _, input := range task.Inputs {
			entry.InputMap[input.ID] = "action"
			if actions[jobID] == nil {
				actions[jobID] = map[string]bool{}
			}
			for _, option := range input.Options {
				value := option
				if alias, ok := legacyActionAliases[option]; ok {
					value = alias
					entry.InputValues[option] = alias
				}
				actions[jobID][value] = true
			}
		}
		mapping.Tasks[task.ID] = entry
	}

	mapping.Agent = MappingAgent{
		ID:            agentID,
		Name:          agentID,
		Labels:        agentLabelsForPlatforms(bundle.Machine, platforms),
		WorkspaceRoot: pickWorkspaceRoot(workspaceRoot, roots),
	}
	if workspaceRoot == "" {
		diagnostics = append(diagnostics, fmt.Sprintf("workspace_root was inferred as %q from the legacy script paths; confirm it before applying", mapping.Agent.WorkspaceRoot))
	}

	for platform := range platforms {
		jobID := platform + "-build"
		mapping.Jobs = append(mapping.Jobs, MappingJob{
			ID:           jobID,
			Name:         strings.ToUpper(platform[:1]) + platform[1:] + " build",
			Labels:       jobLabelsForPlatform(platform),
			Timeout:      defaultPlatformTimeout(platform),
			WorkdirParam: mapping.ProjectParam,
			Script:       defaultPlatformScript(platform),
			ActionParam:  "action",
			Actions:      sortedKeys(actions[jobID]),
		})
	}
	sort.SliceStable(mapping.Jobs, func(i, j int) bool { return mapping.Jobs[i].ID < mapping.Jobs[j].ID })
	sort.Strings(mapping.Unmapped)
	return mapping, diagnostics
}

// pickWorkspaceRoot prefers the explicit flag and otherwise takes the most
// common root observed in the legacy scripts.
func pickWorkspaceRoot(explicit string, roots map[string]int) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	best := ""
	bestCount := 0
	for root, count := range roots {
		if count > bestCount || (count == bestCount && root < best) {
			best, bestCount = root, count
		}
	}
	return best
}

func agentLabelsForPlatforms(machine string, platforms map[string]bool) []string {
	labels := map[string]bool{}
	for platform := range platforms {
		for _, label := range jobLabelsForPlatform(platform) {
			labels[label] = true
		}
	}
	if len(labels) == 0 {
		labels[machine] = true
	}
	return sortedKeys(labels)
}

func jobLabelsForPlatform(platform string) []string {
	switch platform {
	case "android":
		return []string{"android", "linux"}
	case "ios":
		return []string{"ios", "macos"}
	default:
		return []string{platform}
	}
}

func defaultPlatformTimeout(platform string) string {
	if platform == "ios" {
		return "2h"
	}
	return "45m"
}

func defaultPlatformScript(platform string) string {
	if platform == "ios" {
		return "bash \"$BUILDA_WORKSPACE/app-deploy-tools/scripts/builda_ios_entrypoint.sh\" \\\n" +
			"  \"$BUILDA_WORKSPACE/$BUILDA_PARAM_PROJECT_PATH/tools/build_ios.sh\" \"$BUILDA_PARAM_ACTION\"\n"
	}
	return "bash \"$BUILDA_WORKSPACE/$BUILDA_PARAM_PROJECT_PATH/tools/build_android.sh\" \"$BUILDA_PARAM_ACTION\"\n"
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func marshalMapping(mapping *MigrationMapping) ([]byte, error) {
	return yaml.Marshal(mapping)
}

func parseMapping(data []byte) (*MigrationMapping, error) {
	var mapping MigrationMapping
	if err := yaml.Unmarshal(data, &mapping); err != nil {
		return nil, err
	}
	if strings.TrimSpace(mapping.Machine) == "" {
		return nil, fmt.Errorf("mapping requires a machine name")
	}
	if strings.TrimSpace(mapping.ProjectParam) == "" {
		mapping.ProjectParam = "project"
	}
	if strings.TrimSpace(mapping.CatalogID) == "" {
		mapping.CatalogID = "projects"
	}
	return &mapping, nil
}
