package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Builda runs in one of two operational roles. The legacy standalone server is
// retained for migration and testing only.
const (
	RoleController = "controller"
	RoleAgent      = "agent"
	RoleStandalone = "standalone"
)

const (
	controllerConfigName = "controller.yaml"
	agentConfigName      = "agent.yaml"

	controllerServiceName = "builda-controller"
	agentServiceName      = "builda-agent"
)

// roleConfigPath returns the default config file path for a role.
func roleConfigPath(role string) string {
	name := controllerConfigName
	if role == RoleAgent {
		name = agentConfigName
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "builda", name)
	}
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return name
	}
	return filepath.Join(dir, "builda", name)
}

func roleServiceName(role string) string {
	if role == RoleAgent {
		return agentServiceName
	}
	return controllerServiceName
}

// roleHeader is the minimal shape shared by every role config file. It is used
// to detect and reject a config written for the wrong role.
type roleHeader struct {
	Role string `yaml:"role"`
}

func detectConfigRole(data []byte) (string, error) {
	var header roleHeader
	if err := yaml.Unmarshal(data, &header); err != nil {
		return "", err
	}
	role := strings.ToLower(strings.TrimSpace(header.Role))
	switch role {
	case RoleController, RoleAgent:
		return role, nil
	case "":
		return RoleStandalone, nil
	default:
		return "", fmt.Errorf("role must be %q or %q, got %q", RoleController, RoleAgent, role)
	}
}

func requireConfigRole(path string, data []byte, want string) error {
	role, err := detectConfigRole(data)
	if err != nil {
		return err
	}
	if role != want {
		if role == RoleStandalone {
			return fmt.Errorf("%s has no role field; it looks like a legacy standalone config, but a %s config is required", path, want)
		}
		return fmt.Errorf("%s declares role %q but a %s config is required", path, role, want)
	}
	return nil
}

// resolveConfigRelativeDir resolves a possibly relative directory against the
// directory that contains the active config file.
func resolveConfigRelativeDir(configPath, dir, fallback string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = fallback
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	base := filepath.Dir(configPath)
	if strings.TrimSpace(base) == "" || base == "." {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(base, dir))
}

// parseDurationField parses an optional Go duration with a default fallback.
func parseDurationField(field, value string, fallback time.Duration) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", field, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", field)
	}
	return parsed, nil
}

func ensureRoleConfig(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(content), 0o644)
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolPointer(value bool) *bool {
	return &value
}
