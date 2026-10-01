package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// optionPathField is the option metadata key interpreted as a workspace
// relative path. It is validated statically by the controller and re-validated
// against the real filesystem by the agent before execution.
const optionPathField = "path"

var errPathEscapesWorkspace = errors.New("path escapes the workspace root")

// validateRelativeWorkspacePath rejects absolute paths and traversal before a
// value is ever written to config or queued for execution.
func validateRelativeWorkspacePath(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("path must not be empty")
	}
	if trimmed != value {
		return errors.New("path must not have leading or trailing whitespace")
	}
	if strings.ContainsRune(value, '\x00') {
		return errors.New("path must not contain null bytes")
	}
	if strings.Contains(value, `\`) {
		return errors.New(`path must use "/" separators`)
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return errors.New("path must be relative to the agent workspace root")
	}
	if strings.HasPrefix(value, "~") {
		return errors.New("path must not start with ~")
	}
	for _, segment := range strings.Split(value, "/") {
		switch segment {
		case "":
			return errors.New("path must not contain empty segments")
		case ".":
			return errors.New(`path must not contain "." segments`)
		case "..":
			return errors.New(`path must not contain ".." segments`)
		}
	}
	if path.Clean(value) != value {
		return fmt.Errorf("path must be normalized; use %q", path.Clean(value))
	}
	return nil
}

// resolveWorkspacePath validates a relative path against a workspace root and
// returns the absolute directory, rejecting symlinks that leave the root.
func resolveWorkspacePath(root, relative string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("agent workspace_root is not configured")
	}
	if err := validateRelativeWorkspacePath(relative); err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	target := filepath.Join(realRoot, filepath.FromSlash(relative))
	if !withinDir(realRoot, target) {
		return "", errPathEscapesWorkspace
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("workspace path %q does not exist", relative)
		}
		return "", err
	}
	if !withinDir(realRoot, realTarget) {
		return "", errPathEscapesWorkspace
	}
	return realTarget, nil
}

// withinDir reports whether target is root itself or nested beneath it.
func withinDir(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}
