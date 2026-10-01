package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Service target validation. A user daemon pins one absolute executable path
// for its whole lifetime, so an unstable or non-native target is rejected at
// install time instead of failing later inside launchd or systemd.
var errUnstableServiceBinary = errors.New("unstable service binary")

// validateServiceBinary requires an executable regular file that will still be
// there after a reboot. Shell scripts and build-cache binaries are rejected:
// a script would make the daemon a shell process, and a cache binary is
// deleted by the next "go clean" or cache eviction.
func validateServiceBinary(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("service binary path must not be empty")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("service binary %q must be an absolute path", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("service binary %q is not usable: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("service binary %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("service binary %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("service binary %q is not executable", path)
	}
	// The script check runs first so a wrapper script is always reported as
	// such, wherever it happens to live.
	if err := rejectScriptBinary(path); err != nil {
		return err
	}
	return rejectTransientBinaryPath(path)
}

// rejectTransientBinaryPath refuses targets inside a temporary directory or
// the Go build cache, which "go run" and "go test" use.
func rejectTransientBinaryPath(path string) error {
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, string(filepath.Separator)+"go-build") {
		return fmt.Errorf("%w: %q lives in the Go build cache; install a stable binary with \"go install\" and pass --binary \"$(command -v builda)\"", errUnstableServiceBinary, path)
	}
	for _, dir := range serviceTransientRoots() {
		if dir == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			resolved = dir
		}
		if withinDir(resolved, cleaned) || withinDir(filepath.Clean(dir), cleaned) {
			return fmt.Errorf("%w: %q lives under the temporary directory %q and will not survive a reboot", errUnstableServiceBinary, path, dir)
		}
	}
	return nil
}

// serviceTransientRoots is a seam so tests can place a fixture binary inside a
// temporary directory and still exercise the rest of the validation.
var serviceTransientRoots = transientDirs

func transientDirs() []string {
	dirs := []string{os.TempDir(), "/tmp", "/private/var/folders"}
	if cache := strings.TrimSpace(os.Getenv("GOCACHE")); cache != "" {
		dirs = append(dirs, cache)
	}
	return dirs
}

// rejectScriptBinary refuses a script target so the daemon is never a shell.
func rejectScriptBinary(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("service binary %q is not readable: %w", path, err)
	}
	defer file.Close()
	header := make([]byte, 2)
	read, err := file.Read(header)
	if err != nil && read == 0 {
		return fmt.Errorf("service binary %q is empty", path)
	}
	if read >= 2 && header[0] == '#' && header[1] == '!' {
		return fmt.Errorf("%w: %q is a script with a #! interpreter line; the service target must be the Builda executable itself so the daemon never runs a shell", errUnstableServiceBinary, path)
	}
	return nil
}

// validateServiceRoleConfig parses the config the daemon will load so an
// install never produces a unit that cannot start.
func validateServiceRoleConfig(role, configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("service config %q is not readable: %w", configPath, err)
	}
	switch role {
	case RoleController:
		if err := requireConfigRole(configPath, data, RoleController); err != nil {
			return err
		}
		if _, err := parseControllerConfig(data); err != nil {
			return fmt.Errorf("controller config %q is invalid: %w", configPath, err)
		}
	case RoleAgent:
		if err := requireConfigRole(configPath, data, RoleAgent); err != nil {
			return err
		}
		if _, err := parseAgentConfig(data); err != nil {
			return fmt.Errorf("agent config %q is invalid: %w", configPath, err)
		}
	default:
		if _, err := parseConfig(data); err != nil {
			return fmt.Errorf("config %q is invalid: %w", configPath, err)
		}
	}
	return nil
}

// ensureServiceDirectories creates the directories launchd and systemd need
// before the first spawn: the unit directory and the redirected log directory.
func ensureServiceDirectories(spec serviceSpec, artifactPath string) error {
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return fmt.Errorf("create service directory: %w", err)
	}
	if spec.TargetOS != "darwin" {
		return nil
	}
	logDir, err := serviceLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create service log directory %q: %w", logDir, err)
	}
	return nil
}
