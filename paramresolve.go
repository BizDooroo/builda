package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// runWaitParam is the reserved control query parameter of the run API. It is
// never passed to job scripts.
const runWaitParam = "wait"

// ResolvedParameters is the immutable parameter snapshot captured at enqueue.
type ResolvedParameters struct {
	Values      map[string]string       `json:"values"`
	Env         map[string]string       `json:"env"`
	Options     map[string]OptionConfig `json:"options,omitempty"`
	WorkdirPath string                  `json:"workdir_path,omitempty"`
	Paths       map[string]string       `json:"paths,omitempty"`
}

// parameterOptions returns the options a parameter can currently select.
func parameterOptions(cfg ControllerConfig, param ParameterConfig) []OptionConfig {
	if param.Type != paramTypeChoice {
		return nil
	}
	if param.Catalog == "" {
		return param.Options
	}
	catalog, ok := findCatalog(cfg, param.Catalog)
	if !ok {
		return nil
	}
	return filterCatalogOptions(catalog.Options, param.CatalogLabels)
}

// resolveParameters validates caller-supplied values against the job's
// declared parameters and builds the execution environment. Undeclared names,
// repeated values, and invalid choices are rejected before anything is queued.
func resolveParameters(cfg ControllerConfig, job JobConfig, values url.Values) (ResolvedParameters, error) {
	declared := make(map[string]ParameterConfig, len(job.Parameters))
	for _, param := range job.Parameters {
		declared[param.ID] = param
	}
	for key, list := range values {
		if key == runWaitParam {
			continue
		}
		if _, ok := declared[key]; !ok {
			return ResolvedParameters{}, fmt.Errorf("unknown parameter %q for job %q", key, job.ID)
		}
		if len(list) > 1 {
			return ResolvedParameters{}, fmt.Errorf("parameter %q was provided %d times; provide it once", key, len(list))
		}
	}

	resolved := ResolvedParameters{
		Values:  map[string]string{},
		Env:     map[string]string{},
		Options: map[string]OptionConfig{},
		Paths:   map[string]string{},
	}

	for _, param := range job.Parameters {
		value := strings.TrimSpace(values.Get(param.ID))
		if value == "" {
			value = strings.TrimSpace(param.Default)
		}
		if value == "" {
			if param.Required {
				return ResolvedParameters{}, fmt.Errorf("parameter %q is required", param.ID)
			}
			resolved.Values[param.ID] = ""
			resolved.Env[paramEnvName(param.ID)] = ""
			continue
		}
		switch param.Type {
		case paramTypeBoolean:
			normalized, err := normalizeBooleanValue(value)
			if err != nil {
				return ResolvedParameters{}, fmt.Errorf("parameter %q is invalid: %w", param.ID, err)
			}
			value = normalized
		case paramTypeChoice:
			options := parameterOptions(cfg, param)
			selected, ok := findOption(options, value)
			if !ok {
				return ResolvedParameters{}, fmt.Errorf("parameter %q must be one of: %s", param.ID, strings.Join(optionValues(options), ", "))
			}
			resolved.Options[param.ID] = selected
			resolved.Env[paramFieldEnvName(param.ID, optionLabelField)] = selected.Label
			for field, fieldValue := range selected.Values {
				if field == optionPathField {
					if err := validateRelativeWorkspacePath(fieldValue); err != nil {
						return ResolvedParameters{}, fmt.Errorf("parameter %q option %q path is invalid: %w", param.ID, selected.Value, err)
					}
					resolved.Paths[param.ID] = fieldValue
				}
				resolved.Env[paramFieldEnvName(param.ID, field)] = fieldValue
			}
		}
		resolved.Values[param.ID] = value
		resolved.Env[paramEnvName(param.ID)] = value
	}

	if job.WorkdirParam != "" {
		path, ok := resolved.Paths[job.WorkdirParam]
		if !ok {
			return ResolvedParameters{}, fmt.Errorf("job %q requires parameter %q to select an option with a path", job.ID, job.WorkdirParam)
		}
		resolved.WorkdirPath = path
	}
	if len(resolved.Options) == 0 {
		resolved.Options = nil
	}
	if len(resolved.Paths) == 0 {
		resolved.Paths = nil
	}
	return resolved, nil
}

// resolveHistoricalParameters resolves the parameters of a run that already
// happened. It still rejects a name the job does not declare, so history stays
// filterable, but it keeps a recorded value whose option has since been
// retired instead of discarding the run. Re-running such a run is rejected at
// enqueue time, which is where that check belongs.
func resolveHistoricalParameters(cfg ControllerConfig, job JobConfig, values map[string]string) (ResolvedParameters, []string, error) {
	declared := make(map[string]ParameterConfig, len(job.Parameters))
	for _, param := range job.Parameters {
		declared[param.ID] = param
	}
	for name := range values {
		if _, ok := declared[name]; !ok {
			return ResolvedParameters{}, nil, fmt.Errorf("job %q does not declare parameter %q", job.ID, name)
		}
	}

	resolved := ResolvedParameters{
		Values:  map[string]string{},
		Env:     map[string]string{},
		Options: map[string]OptionConfig{},
		Paths:   map[string]string{},
	}
	retired := make([]string, 0)

	for _, param := range job.Parameters {
		value := strings.TrimSpace(values[param.ID])
		if value == "" {
			value = strings.TrimSpace(param.Default)
		}
		if value == "" {
			resolved.Values[param.ID] = ""
			resolved.Env[paramEnvName(param.ID)] = ""
			continue
		}
		if param.Type == paramTypeChoice {
			if selected, ok := findOption(parameterOptions(cfg, param), value); ok {
				resolved.Options[param.ID] = selected
				resolved.Env[paramFieldEnvName(param.ID, optionLabelField)] = selected.Label
				for field, fieldValue := range selected.Values {
					if field == optionPathField {
						resolved.Paths[param.ID] = fieldValue
					}
					resolved.Env[paramFieldEnvName(param.ID, field)] = fieldValue
				}
			} else {
				retired = append(retired, fmt.Sprintf("%s=%s", param.ID, value))
			}
		}
		resolved.Values[param.ID] = value
		resolved.Env[paramEnvName(param.ID)] = value
	}
	if job.WorkdirParam != "" {
		resolved.WorkdirPath = resolved.Paths[job.WorkdirParam]
	}
	if len(resolved.Options) == 0 {
		resolved.Options = nil
	}
	if len(resolved.Paths) == 0 {
		resolved.Paths = nil
	}
	sort.Strings(retired)
	return resolved, retired, nil
}

func findOption(options []OptionConfig, value string) (OptionConfig, bool) {
	for _, option := range options {
		if option.Value == value {
			return option, true
		}
	}
	return OptionConfig{}, false
}

func optionValues(options []OptionConfig) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	sort.Strings(values)
	return values
}

// parameterValuesToQuery converts a stored parameter snapshot back into query
// values so a historical run can be re-queued against the current config.
func parameterValuesToQuery(values map[string]string) url.Values {
	query := url.Values{}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if values[name] == "" {
			continue
		}
		query.Set(name, values[name])
	}
	return query
}
