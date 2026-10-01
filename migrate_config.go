package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// buildRoleConfigs turns one or more reviewed mappings into a controller
// config and one agent config per machine. Shared catalogs are merged so a
// project present on both platforms becomes a single catalog entry carrying
// both platform labels, which makes it appear in every applicable job list.
func buildRoleConfigs(bundles []*MigrationBundle, mappings []*MigrationMapping) (ControllerConfig, map[string]AgentConfig, error) {
	if len(bundles) != len(mappings) {
		return ControllerConfig{}, nil, fmt.Errorf("each bundle needs exactly one mapping")
	}
	catalogID := "projects"
	projectParam := "project"
	options := map[string]*OptionConfig{}
	jobs := map[string]*MappingJob{}
	agents := map[string]AgentConfig{}
	definitions := map[string]AgentDefinition{}

	for index, mapping := range mappings {
		bundle := bundles[index]
		if mapping.CatalogID != "" {
			catalogID = mapping.CatalogID
		}
		if mapping.ProjectParam != "" {
			projectParam = mapping.ProjectParam
		}
		for i := range mapping.Jobs {
			job := mapping.Jobs[i]
			if existing, ok := jobs[job.ID]; ok {
				existing.Actions = mergeSorted(existing.Actions, job.Actions)
				continue
			}
			copied := job
			jobs[job.ID] = &copied
		}
		for taskID, task := range mapping.Tasks {
			if task.Project == "" {
				continue
			}
			path := task.ProjectPath
			if path == "" {
				path = task.Project
			}
			if err := validateRelativeWorkspacePath(path); err != nil {
				return ControllerConfig{}, nil, fmt.Errorf("task %q project path %q is invalid: %w", taskID, path, err)
			}
			option, ok := options[task.Project]
			if !ok {
				option = &OptionConfig{
					Value:  task.Project,
					Label:  projectDisplayLabel(task.Project),
					Values: map[string]string{optionPathField: path},
				}
				options[task.Project] = option
			}
			option.Labels = mergeSorted(option.Labels, task.Labels)
		}
		agentCfg := AgentConfig{Role: RoleAgent, Agent: AgentSettings{
			ID:            mapping.Agent.ID,
			Name:          mapping.Agent.Name,
			ControllerURL: "http://127.0.0.1:28080",
			SpoolDir:      "spool",
			WorkspaceRoot: mapping.Agent.WorkspaceRoot,
			ScriptHeader:  bundle.ScriptHeader,
		}}
		if err := validateAgentConfig(&agentCfg); err != nil {
			return ControllerConfig{}, nil, fmt.Errorf("generated agent config for %q is invalid: %w", mapping.Machine, err)
		}
		agents[mapping.Machine] = agentCfg
		definitions[mapping.Agent.ID] = AgentDefinition{
			ID:     mapping.Agent.ID,
			Name:   mapping.Agent.Name,
			Labels: mapping.Agent.Labels,
		}
	}

	catalog := CatalogConfig{ID: catalogID, Name: "Projects"}
	for _, value := range sortedKeys(options) {
		catalog.Options = append(catalog.Options, *options[value])
	}

	cfg := ControllerConfig{Role: RoleController, Server: ControllerServer{
		Addresses:  []string{"127.0.0.1:28080"},
		StateDir:   "state",
		MaxHistory: defaultMaxHistory,
	}}
	cfg.Catalogs = []CatalogConfig{catalog}
	for _, id := range sortedKeys(jobs) {
		cfg.Jobs = append(cfg.Jobs, buildMigratedJob(*jobs[id], catalogID, projectParam))
	}
	for _, id := range sortedKeys(definitions) {
		cfg.Agents = append(cfg.Agents, definitions[id])
	}
	if err := validateControllerConfig(&cfg); err != nil {
		return ControllerConfig{}, nil, fmt.Errorf("generated controller config is invalid: %w", err)
	}
	return cfg, agents, nil
}

func buildMigratedJob(job MappingJob, catalogID, projectParam string) JobConfig {
	platformLabel := primaryPlatformLabel(job)
	result := JobConfig{
		ID:           job.ID,
		Name:         job.Name,
		Labels:       job.Labels,
		Timeout:      job.Timeout,
		Script:       job.Script,
		WorkdirParam: job.WorkdirParam,
	}
	result.Parameters = append(result.Parameters, ParameterConfig{
		ID:            projectParam,
		Name:          "Project",
		Type:          paramTypeChoice,
		Required:      true,
		Catalog:       catalogID,
		CatalogLabels: []string{platformLabel},
	})
	if job.ActionParam == "" || len(job.Actions) == 0 {
		return result
	}
	action := ParameterConfig{ID: job.ActionParam, Name: "Action", Type: paramTypeChoice}
	for _, value := range job.Actions {
		label := value
		if job.ActionLabels != nil && job.ActionLabels[value] != "" {
			label = job.ActionLabels[value]
		}
		action.Options = append(action.Options, OptionConfig{Value: value, Label: label})
	}
	action.Default = defaultActionValue(job.Actions)
	result.Parameters = append(result.Parameters, action)
	return result
}

// primaryPlatformLabel is the label used to filter catalog options for a job.
func primaryPlatformLabel(job MappingJob) string {
	if platform, ok := strings.CutSuffix(job.ID, "-build"); ok && platform != "" {
		return platform
	}
	if len(job.Labels) > 0 {
		return job.Labels[0]
	}
	return job.ID
}

// defaultActionValue picks the conventional default action when it is present.
func defaultActionValue(actions []string) string {
	for _, preferred := range []string{"debug", "ad-hoc"} {
		for _, action := range actions {
			if action == preferred {
				return preferred
			}
		}
	}
	if len(actions) > 0 {
		return actions[0]
	}
	return ""
}

func projectDisplayLabel(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '_' || r == '-' || r == '/' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	if len(parts) == 0 {
		return value
	}
	return strings.Join(parts, " ")
}

func mergeSorted(current, extra []string) []string {
	seen := map[string]bool{}
	for _, value := range append(append([]string{}, current...), extra...) {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = true
		}
	}
	merged := make([]string, 0, len(seen))
	for value := range seen {
		merged = append(merged, value)
	}
	sort.Strings(merged)
	return merged
}

// writeRoleConfigs writes the generated documents into an output directory.
func writeRoleConfigs(outDir string, cfg ControllerConfig, agents map[string]AgentConfig) ([]string, error) {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	written := make([]string, 0, 1+len(agents))
	data, err := marshalControllerConfig(cfg)
	if err != nil {
		return nil, err
	}
	controllerPath := filepath.Join(outDir, controllerConfigName)
	if err := writeFileAtomicSync(controllerPath, data, 0o600); err != nil {
		return nil, err
	}
	written = append(written, controllerPath)
	for _, machine := range sortedKeys(agents) {
		encoded, err := yaml.Marshal(agents[machine])
		if err != nil {
			return nil, err
		}
		path := filepath.Join(outDir, "agent-"+machine+".yaml")
		if err := writeFileAtomicSync(path, encoded, 0o600); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

func marshalAgentConfig(cfg AgentConfig) ([]byte, error) {
	cfg.Role = RoleAgent
	return yaml.Marshal(cfg)
}
