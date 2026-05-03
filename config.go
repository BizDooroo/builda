package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func defaultConfigPath() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "builda", "config.yaml")
	}
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return "config.yaml"
	}
	return filepath.Join(dir, "builda", "config.yaml")
}

func ensureDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(sampleConfig), 0644)
}

func resolveLogDir(configPath, logDir string) string {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		logDir = "logs"
	}
	if filepath.IsAbs(logDir) {
		return filepath.Clean(logDir)
	}
	base := filepath.Dir(configPath)
	if strings.TrimSpace(base) == "" || base == "." {
		return filepath.Clean(logDir)
	}
	return filepath.Clean(filepath.Join(base, logDir))
}

func statFileStamp(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, nil
}

func (s fileStamp) equal(other fileStamp) bool {
	return s.size == other.size && s.modTime.Equal(other.modTime)
}

func versionInfo() string {
	v := strings.TrimSpace(version)
	rev := strings.TrimSpace(commit)
	built := strings.TrimSpace(date)
	modified := ""

	if info, ok := debug.ReadBuildInfo(); ok {
		if (v == "" || v == "dev") && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if rev == "" {
					rev = setting.Value
				}
			case "vcs.time":
				if built == "" {
					built = setting.Value
				}
			case "vcs.modified":
				if setting.Value == "true" {
					modified = " dirty"
				}
			}
		}
	}

	if v == "" {
		v = "dev"
	}
	parts := []string{"builda " + v}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		parts = append(parts, "commit "+rev+modified)
	}
	if built != "" {
		parts = append(parts, "built "+built)
	}
	return strings.Join(parts, ", ")
}

func configuredListenAddresses(server ServerConfig) []string {
	addrs := make([]string, 0, 1+len(server.Addresses))
	if strings.TrimSpace(server.Address) != "" {
		addrs = append(addrs, server.Address)
	}
	addrs = append(addrs, server.Addresses...)
	return normalizeListenAddresses(addrs)
}

func normalizeListenAddresses(addresses []string) []string {
	addrs := make([]string, 0, len(addresses))
	seen := map[string]bool{}
	for _, addr := range addresses {
		addr = strings.TrimSpace(addr)
		if addr == "" || seen[addr] {
			continue
		}
		addrs = append(addrs, addr)
		seen[addr] = true
	}
	if len(addrs) == 0 {
		return []string{defaultListenAddress}
	}
	return addrs
}

func resolveListenAddresses(configAddresses []string, flagAddresses []string) []string {
	if len(flagAddresses) == 0 {
		return normalizeListenAddresses(configAddresses)
	}
	return normalizeListenAddresses(flagAddresses)
}
func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return parseConfig(data)
}

func loadRuntimeConfig(path string) (Config, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return Config{}, err
	}
	return normalizeRuntimeConfig(path, cfg), nil
}

func normalizeRuntimeConfig(configPath string, cfg Config) Config {
	listenAddrs := configuredListenAddresses(cfg.Server)
	if len(listenAddrs) == 1 &&
		listenAddrs[0] == defaultListenAddress &&
		strings.TrimSpace(cfg.Server.Address) == "" &&
		len(cfg.Server.Addresses) == 0 {
		cfg.Server.Address = defaultListenAddress
	}
	if cfg.Server.LogDir == "" {
		cfg.Server.LogDir = "logs"
	}
	cfg.Server.LogDir = resolveLogDir(configPath, cfg.Server.LogDir)
	for i := range cfg.Tasks {
		cfg.Tasks[i].ScriptHeader = cfg.Server.ScriptHeader
	}
	return cfg
}

func configEditingEnabled(cfg Config) bool {
	return strings.TrimSpace(cfg.Server.ConfigPassword) != ""
}

func (a *App) currentConfigPassword() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return strings.TrimSpace(a.cfg.Server.ConfigPassword)
}

func (a *App) configEditingEnabled() bool {
	return a.currentConfigPassword() != ""
}

func parseConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.Server.ScriptHeader = strings.TrimRight(cfg.Server.ScriptHeader, "\r\n")
	if strings.TrimSpace(cfg.Server.ScriptHeader) == "" {
		cfg.Server.ScriptHeader = defaultScriptHeader
	}
	seen := map[string]bool{}
	for i, task := range cfg.Tasks {
		script := strings.TrimSpace(task.Script)
		if strings.TrimSpace(task.ID) == "" {
			return Config{}, fmt.Errorf("tasks[%d].id is required", i)
		}
		if seen[task.ID] {
			return Config{}, fmt.Errorf("duplicate task id %q", task.ID)
		}
		if script == "" {
			return Config{}, fmt.Errorf("tasks[%d].script is required", i)
		}
		if strings.TrimSpace(task.Timeout) != "" {
			if _, err := time.ParseDuration(task.Timeout); err != nil {
				return Config{}, fmt.Errorf("tasks[%d].timeout is invalid: %w", i, err)
			}
		}
		inputIDs := map[string]bool{}
		inputEnvs := map[string]string{}
		for j, input := range task.Inputs {
			inputID := strings.TrimSpace(input.ID)
			if inputID == "" {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id is required", i, j)
			}
			if inputID == taskRunWaitParam {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id %q is reserved for the task run API", i, j, inputID)
			}
			if !validInputID(inputID) {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id %q must contain only letters, digits, underscores, and hyphens", i, j, inputID)
			}
			if inputIDs[inputID] {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id duplicates %q", i, j, inputID)
			}
			envName := taskInputEnvName(inputID)
			if previous, ok := inputEnvs[envName]; ok {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].id %q conflicts with %q as %s", i, j, inputID, previous, envName)
			}
			inputType, err := normalizeInputType(input.Type)
			if err != nil {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].type is invalid: %w", i, j, err)
			}
			cfg.Tasks[i].Inputs[j].ID = inputID
			cfg.Tasks[i].Inputs[j].Type = inputType
			if strings.TrimSpace(input.Name) == "" {
				cfg.Tasks[i].Inputs[j].Name = inputID
			}
			if inputType == "choice" {
				options, err := normalizeChoiceOptions(input.Options)
				if err != nil {
					return Config{}, fmt.Errorf("tasks[%d].inputs[%d].options are invalid: %w", i, j, err)
				}
				cfg.Tasks[i].Inputs[j].Options = options
				if input.Default != "" && !containsString(options, input.Default) {
					return Config{}, fmt.Errorf("tasks[%d].inputs[%d].default must be one of options", i, j)
				}
			} else if len(input.Options) > 0 {
				return Config{}, fmt.Errorf("tasks[%d].inputs[%d].options are only valid for choice inputs", i, j)
			}
			inputIDs[inputID] = true
			inputEnvs[envName] = inputID
		}
		seen[task.ID] = true
		if task.Name == "" {
			cfg.Tasks[i].Name = task.ID
		}
	}
	return cfg, nil
}

func normalizeInputType(inputType string) (string, error) {
	inputType = strings.ToLower(strings.TrimSpace(inputType))
	switch inputType {
	case "", "string", "input":
		return "string", nil
	case "choice":
		return "choice", nil
	default:
		return "", fmt.Errorf("must be string, input, or choice")
	}
}

func normalizeChoiceOptions(options []string) ([]string, error) {
	if len(options) == 0 {
		return nil, errors.New("choice inputs require at least one option")
	}
	normalized := make([]string, 0, len(options))
	seen := map[string]bool{}
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			return nil, errors.New("choice options must not be empty")
		}
		if seen[option] {
			return nil, fmt.Errorf("duplicate option %q", option)
		}
		seen[option] = true
		normalized = append(normalized, option)
	}
	return normalized, nil
}

func validInputID(inputID string) bool {
	if inputID == "" {
		return false
	}
	for _, r := range inputID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func taskInputEnvName(inputID string) string {
	var b strings.Builder
	b.WriteString("BUILDA_INPUT_")
	for _, r := range inputID {
		if r == '-' {
			b.WriteByte('_')
			continue
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func buildTaskMap(tasks []TaskConfig) map[string]TaskConfig {
	taskMap := make(map[string]TaskConfig, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
	}
	return taskMap
}

func collectTaskInputs(task TaskConfig, values url.Values) (map[string]string, error) {
	allowed := map[string]TaskInputConfig{}
	for _, input := range task.Inputs {
		allowed[input.ID] = input
	}
	for key := range values {
		if key == "task_id" || key == taskRunWaitParam {
			continue
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unknown input %q for task %q", key, task.ID)
		}
	}

	inputs := make(map[string]string, len(task.Inputs))
	for _, input := range task.Inputs {
		value := values.Get(input.ID)
		if value == "" {
			value = input.Default
		}
		if input.Required && value == "" {
			return nil, fmt.Errorf("input %q is required", input.ID)
		}
		if input.Type == "choice" && value != "" && !containsString(input.Options, value) {
			return nil, fmt.Errorf("input %q must be one of: %s", input.ID, strings.Join(input.Options, ", "))
		}
		inputs[input.ID] = value
	}
	return inputs, nil
}

func taskRunWaitRequested(values url.Values) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(values.Get(taskRunWaitParam)))
	switch value {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be 1 or 0", taskRunWaitParam)
	}
}
