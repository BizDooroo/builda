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

// executableMagics are the first bytes of the native executable formats a
// Builda build produces: ELF, the four Mach-O variants, and a Mach-O fat
// binary.
var executableMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},
	{0xfe, 0xed, 0xfa, 0xce},
	{0xce, 0xfa, 0xed, 0xfe},
	{0xfe, 0xed, 0xfa, 0xcf},
	{0xcf, 0xfa, 0xed, 0xfe},
	{0xca, 0xfe, 0xba, 0xbe},
	{0xbe, 0xba, 0xfe, 0xca},
}

// rejectScriptBinary refuses a script target so the daemon is never a shell.
// A shebang is the obvious case; a text file without one is caught too,
// because it would otherwise only fail later as an exec format error at spawn
// time, where the cause is far from obvious.
func rejectScriptBinary(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("service binary %q is not readable: %w", path, err)
	}
	defer file.Close()
	header := make([]byte, 4)
	read, err := file.Read(header)
	if err != nil && read == 0 {
		return fmt.Errorf("service binary %q is empty", path)
	}
	header = header[:read]
	if read >= 2 && header[0] == '#' && header[1] == '!' {
		return fmt.Errorf("%w: %q is a script with a #! interpreter line; the service target must be the Builda executable itself so the daemon never runs a shell", errUnstableServiceBinary, path)
	}
	for _, magic := range executableMagics {
		if read >= len(magic) && string(header[:len(magic)]) == string(magic) {
			return nil
		}
	}
	if looksLikeText(header) {
		return fmt.Errorf("%w: %q starts with text rather than native executable code; the service target must be the Builda executable itself", errUnstableServiceBinary, path)
	}
	return nil
}

// looksLikeText reports whether every leading byte is printable ASCII or
// common whitespace, which no native executable header is.
func looksLikeText(header []byte) bool {
	if len(header) == 0 {
		return false
	}
	for _, b := range header {
		if b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
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
