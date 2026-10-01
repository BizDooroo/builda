package main

import (
	"fmt"
	"strings"
	"time"
)

const (
	paramTypeString  = "string"
	paramTypeChoice  = "choice"
	paramTypeBoolean = "boolean"
)

// validateControllerConfig normalizes and validates the whole controller
// document. It mutates cfg in place so callers always observe normalized
// labels, option labels, and parameter types.
func validateControllerConfig(cfg *ControllerConfig) error {
	if cfg.Server.MaxHistory < 0 {
		return fmt.Errorf("server.max_history must be zero or greater")
	}
	if _, err := parseDurationField("server.heartbeat_interval", cfg.Server.HeartbeatInterval, defaultHeartbeatInterval); err != nil {
		return err
	}
	if _, err := parseDurationField("server.offline_after", cfg.Server.OfflineAfter, defaultOfflineAfter); err != nil {
		return err
	}
	if _, err := parseDurationField("server.long_poll_timeout", cfg.Server.LongPollTimeout, defaultLongPollTimeout); err != nil {
		return err
	}
	if err := validateCatalogs(cfg); err != nil {
		return err
	}
	if err := validateJobs(cfg); err != nil {
		return err
	}
	return validateAgentDefinitions(cfg)
}

func validateCatalogs(cfg *ControllerConfig) error {
	seen := map[string]bool{}
	for i := range cfg.Catalogs {
		catalog := &cfg.Catalogs[i]
		catalog.ID = strings.TrimSpace(catalog.ID)
		if !validIdentifier(catalog.ID) {
			return fmt.Errorf("catalogs[%d].id %q must contain only letters, digits, underscores, and hyphens", i, catalog.ID)
		}
		if seen[catalog.ID] {
			return fmt.Errorf("duplicate catalog id %q", catalog.ID)
		}
		seen[catalog.ID] = true
		if strings.TrimSpace(catalog.Name) == "" {
			catalog.Name = catalog.ID
		}
		if len(catalog.Options) == 0 {
			return fmt.Errorf("catalogs[%d] %q requires at least one option", i, catalog.ID)
		}
		if err := normalizeOptions(catalog.Options, fmt.Sprintf("catalogs[%d] %q", i, catalog.ID)); err != nil {
			return err
		}
	}
	return nil
}

// normalizeOptions validates and normalizes an option list in place.
func normalizeOptions(options []OptionConfig, owner string) error {
	seen := map[string]bool{}
	for i := range options {
		option := &options[i]
		option.Value = strings.TrimSpace(option.Value)
		if option.Value == "" {
			return fmt.Errorf("%s options[%d].value is required", owner, i)
		}
		if seen[option.Value] {
			return fmt.Errorf("%s has duplicate option value %q", owner, option.Value)
		}
		seen[option.Value] = true
		if strings.TrimSpace(option.Label) == "" {
			option.Label = option.Value
		}
		labels, err := normalizeLabels(option.Labels)
		if err != nil {
			return fmt.Errorf("%s options[%d].labels are invalid: %w", owner, i, err)
		}
		option.Labels = labels
		for field, value := range option.Values {
			if !validIdentifier(field) {
				return fmt.Errorf("%s options[%d].values key %q must contain only letters, digits, underscores, and hyphens", owner, i, field)
			}
			if field == optionPathField {
				if err := validateRelativeWorkspacePath(value); err != nil {
					return fmt.Errorf("%s options[%d].values.path is invalid: %w", owner, i, err)
				}
			}
		}
	}
	return nil
}

// normalizeLabels lowercases, trims, de-duplicates, and bounds a label list.
func normalizeLabels(labels []string) ([]string, error) {
	if len(labels) > maxQueueLabelCount {
		return nil, fmt.Errorf("at most %d labels are allowed", maxQueueLabelCount)
	}
	normalized := make([]string, 0, len(labels))
	seen := map[string]bool{}
	for _, label := range labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == "" {
			return nil, fmt.Errorf("labels must not be empty")
		}
		if !validIdentifier(label) {
			return nil, fmt.Errorf("label %q must contain only letters, digits, underscores, and hyphens", label)
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		normalized = append(normalized, label)
	}
	return normalized, nil
}

func validateJobs(cfg *ControllerConfig) error {
	seen := map[string]bool{}
	for i := range cfg.Jobs {
		job := &cfg.Jobs[i]
		job.ID = strings.TrimSpace(job.ID)
		if !validIdentifier(job.ID) {
			return fmt.Errorf("jobs[%d].id %q must contain only letters, digits, underscores, and hyphens", i, job.ID)
		}
		if seen[job.ID] {
			return fmt.Errorf("duplicate job id %q", job.ID)
		}
		seen[job.ID] = true
		if strings.TrimSpace(job.Name) == "" {
			job.Name = job.ID
		}
		if strings.TrimSpace(job.Script) == "" {
			return fmt.Errorf("jobs[%d] %q requires a script", i, job.ID)
		}
		if strings.TrimSpace(job.Timeout) != "" {
			timeout, err := time.ParseDuration(job.Timeout)
			if err != nil {
				return fmt.Errorf("jobs[%d] %q timeout is invalid: %w", i, job.ID, err)
			}
			if timeout <= 0 {
				return fmt.Errorf("jobs[%d] %q timeout must be greater than zero", i, job.ID)
			}
		}
		labels, err := normalizeLabels(job.Labels)
		if err != nil {
			return fmt.Errorf("jobs[%d] %q labels are invalid: %w", i, job.ID, err)
		}
		job.Labels = labels
		if err := validateJobParameters(*cfg, job, i); err != nil {
			return err
		}
	}
	return nil
}

func validateJobParameters(cfg ControllerConfig, job *JobConfig, index int) error {
	owner := fmt.Sprintf("jobs[%d] %q", index, job.ID)
	seen := map[string]bool{}
	collisions := newEnvCollisionChecker()
	pathParams := map[string]bool{}

	for j := range job.Parameters {
		param := &job.Parameters[j]
		param.ID = strings.TrimSpace(param.ID)
		if !validIdentifier(param.ID) {
			return fmt.Errorf("%s parameters[%d].id %q must contain only letters, digits, underscores, and hyphens", owner, j, param.ID)
		}
		if seen[param.ID] {
			return fmt.Errorf("%s has duplicate parameter id %q", owner, param.ID)
		}
		seen[param.ID] = true
		if strings.TrimSpace(param.Name) == "" {
			param.Name = param.ID
		}
		paramType, err := normalizeParameterType(param.Type)
		if err != nil {
			return fmt.Errorf("%s parameters[%d].type is invalid: %w", owner, j, err)
		}
		param.Type = paramType

		options, err := validateParameterSource(cfg, param, owner, j)
		if err != nil {
			return err
		}
		if err := validateParameterDefault(*param, options, owner, j); err != nil {
			return err
		}
		for _, name := range paramEnvNames(*param, options) {
			if err := collisions.add(name, fmt.Sprintf("%s parameters[%d] %q", owner, j, param.ID)); err != nil {
				return err
			}
		}
		if optionsCarryPath(options) {
			pathParams[param.ID] = true
		}
	}

	job.WorkdirParam = strings.TrimSpace(job.WorkdirParam)
	if job.WorkdirParam != "" && !pathParams[job.WorkdirParam] {
		return fmt.Errorf("%s workdir_param %q must name a parameter whose options declare a values.path entry", owner, job.WorkdirParam)
	}
	return nil
}

// validateParameterSource resolves the option source of a parameter and
// returns every option it can select.
func validateParameterSource(cfg ControllerConfig, param *ParameterConfig, owner string, index int) ([]OptionConfig, error) {
	param.Catalog = strings.TrimSpace(param.Catalog)
	catalogLabels, err := normalizeLabels(param.CatalogLabels)
	if err != nil {
		return nil, fmt.Errorf("%s parameters[%d].catalog_labels are invalid: %w", owner, index, err)
	}
	param.CatalogLabels = catalogLabels

	if param.Type != paramTypeChoice {
		if len(param.Options) > 0 {
			return nil, fmt.Errorf("%s parameters[%d] %q declares options but is not a choice parameter", owner, index, param.ID)
		}
		if param.Catalog != "" {
			return nil, fmt.Errorf("%s parameters[%d] %q declares a catalog but is not a choice parameter", owner, index, param.ID)
		}
		if len(param.CatalogLabels) > 0 {
			return nil, fmt.Errorf("%s parameters[%d] %q declares catalog_labels but is not a choice parameter", owner, index, param.ID)
		}
		return nil, nil
	}

	hasInline := len(param.Options) > 0
	hasCatalog := param.Catalog != ""
	switch {
	case hasInline && hasCatalog:
		return nil, fmt.Errorf("%s parameters[%d] %q must declare either inline options or a catalog, not both", owner, index, param.ID)
	case !hasInline && !hasCatalog:
		return nil, fmt.Errorf("%s parameters[%d] %q requires inline options or a catalog", owner, index, param.ID)
	case hasInline:
		if len(param.CatalogLabels) > 0 {
			return nil, fmt.Errorf("%s parameters[%d] %q declares catalog_labels without a catalog", owner, index, param.ID)
		}
		if err := normalizeOptions(param.Options, fmt.Sprintf("%s parameters[%d] %q", owner, index, param.ID)); err != nil {
			return nil, err
		}
		return param.Options, nil
	default:
		catalog, ok := findCatalog(cfg, param.Catalog)
		if !ok {
			return nil, fmt.Errorf("%s parameters[%d] %q references unknown catalog %q", owner, index, param.ID, param.Catalog)
		}
		if len(filterCatalogOptions(catalog.Options, param.CatalogLabels)) == 0 {
			return nil, fmt.Errorf("%s parameters[%d] %q selects no options from catalog %q with labels %v", owner, index, param.ID, param.Catalog, param.CatalogLabels)
		}
		// Collision checking considers every catalog option so that adding a
		// new catalog entry later cannot silently introduce a conflict.
		return catalog.Options, nil
	}
}

func validateParameterDefault(param ParameterConfig, options []OptionConfig, owner string, index int) error {
	if param.Default == "" {
		return nil
	}
	switch param.Type {
	case paramTypeBoolean:
		if _, err := normalizeBooleanValue(param.Default); err != nil {
			return fmt.Errorf("%s parameters[%d] %q default is invalid: %w", owner, index, param.ID, err)
		}
	case paramTypeChoice:
		if !optionValueExists(filterCatalogOptions(options, param.CatalogLabels), param.Default) {
			return fmt.Errorf("%s parameters[%d] %q default %q is not a selectable option", owner, index, param.ID, param.Default)
		}
	}
	return nil
}

// filterCatalogOptions keeps options that carry every requested label.
func filterCatalogOptions(options []OptionConfig, required []string) []OptionConfig {
	if len(required) == 0 {
		return options
	}
	filtered := make([]OptionConfig, 0, len(options))
	for _, option := range options {
		if labelsSatisfied(required, option.Labels) {
			filtered = append(filtered, option)
		}
	}
	return filtered
}

// labelsSatisfied reports whether available contains every required label.
func labelsSatisfied(required, available []string) bool {
	if len(required) == 0 {
		return true
	}
	have := make(map[string]bool, len(available))
	for _, label := range available {
		have[label] = true
	}
	for _, label := range required {
		if !have[label] {
			return false
		}
	}
	return true
}

func optionValueExists(options []OptionConfig, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func optionsCarryPath(options []OptionConfig) bool {
	if len(options) == 0 {
		return false
	}
	for _, option := range options {
		if _, ok := option.Values[optionPathField]; !ok {
			return false
		}
	}
	return true
}

func normalizeParameterType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", paramTypeString, "input":
		return paramTypeString, nil
	case paramTypeChoice:
		return paramTypeChoice, nil
	case paramTypeBoolean, "bool":
		return paramTypeBoolean, nil
	default:
		return "", fmt.Errorf("must be string, choice, or boolean")
	}
}

func normalizeBooleanValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return "true", nil
	case "0", "false", "no", "off":
		return "false", nil
	default:
		return "", fmt.Errorf("must be true or false")
	}
}

func validateAgentDefinitions(cfg *ControllerConfig) error {
	seen := map[string]bool{}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		agent.ID = strings.TrimSpace(agent.ID)
		if !validIdentifier(agent.ID) {
			return fmt.Errorf("agents[%d].id %q must contain only letters, digits, underscores, and hyphens", i, agent.ID)
		}
		if seen[agent.ID] {
			return fmt.Errorf("duplicate agent id %q", agent.ID)
		}
		seen[agent.ID] = true
		if strings.TrimSpace(agent.Name) == "" {
			agent.Name = agent.ID
		}
		labels, err := normalizeLabels(agent.Labels)
		if err != nil {
			return fmt.Errorf("agents[%d] %q labels are invalid: %w", i, agent.ID, err)
		}
		agent.Labels = labels
	}
	return nil
}
