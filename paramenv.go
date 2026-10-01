package main

import (
	"fmt"
	"sort"
	"strings"
)

// Parameter environment naming. Job scripts never receive parameter values
// through string interpolation; every value is passed as a process
// environment variable so shell substitution cannot be injected.
const (
	paramEnvPrefix   = "BUILDA_PARAM_"
	buildaEnvPrefix  = "BUILDA_"
	optionLabelField = "label"
)

// reservedEnvNames are injected by the agent and may never be produced by a
// declared parameter.
var reservedEnvNames = []string{
	"BUILDA_AGENT_ID",
	"BUILDA_CONTROLLER_URL",
	"BUILDA_EXECUTION_ID",
	"BUILDA_JOB_ID",
	"BUILDA_JOB_NAME",
	"BUILDA_LOG_PATH",
	"BUILDA_WORKSPACE",
}

func isReservedEnvName(name string) bool {
	for _, reserved := range reservedEnvNames {
		if reserved == name {
			return true
		}
	}
	return false
}

// normalizeEnvSegment uppercases an identifier and maps hyphens to
// underscores so that "ad-hoc" and "ad_hoc" normalize to the same segment.
// Collisions produced by this normalization are rejected during validation.
func normalizeEnvSegment(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '-':
			b.WriteByte('_')
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func paramEnvName(paramID string) string {
	return paramEnvPrefix + normalizeEnvSegment(paramID)
}

func paramFieldEnvName(paramID, field string) string {
	return paramEnvName(paramID) + "_" + normalizeEnvSegment(field)
}

// validIdentifier accepts the limited character set used for job, parameter,
// catalog, agent, label, and option-field identifiers.
func validIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			continue
		default:
			return false
		}
	}
	return true
}

// paramEnvNames returns every environment variable name a parameter can emit,
// including the metadata fields of each option it may select.
func paramEnvNames(param ParameterConfig, options []OptionConfig) []string {
	names := []string{paramEnvName(param.ID)}
	if len(options) == 0 {
		return names
	}
	fields := map[string]bool{optionLabelField: true}
	for _, option := range options {
		for field := range option.Values {
			fields[field] = true
		}
	}
	keys := make([]string, 0, len(fields))
	for field := range fields {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	for _, field := range keys {
		names = append(names, paramFieldEnvName(param.ID, field))
	}
	return names
}

// envCollisionChecker rejects two declarations that normalize to the same
// environment variable name, and any declaration that shadows a reserved name.
type envCollisionChecker struct {
	owners map[string]string
}

func newEnvCollisionChecker() *envCollisionChecker {
	return &envCollisionChecker{owners: map[string]string{}}
}

func (c *envCollisionChecker) add(name, owner string) error {
	if isReservedEnvName(name) {
		return fmt.Errorf("%s produces reserved environment variable %s", owner, name)
	}
	if previous, ok := c.owners[name]; ok {
		return fmt.Errorf("%s produces environment variable %s already produced by %s", owner, name, previous)
	}
	c.owners[name] = owner
	return nil
}

// sanitizeInheritedEnv drops every inherited BUILDA_* variable so a parent
// process cannot spoof parameter values or execution identity.
func sanitizeInheritedEnv(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, buildaEnvPrefix) {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// sortedEnv renders a name/value map as a deterministic NAME=value slice.
func sortedEnv(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries, name+"="+values[name])
	}
	return entries
}
