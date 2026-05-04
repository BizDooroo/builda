package main

import (
	"embed"
	"fmt"
	"os"
	"time"
)

//go:embed all:web/dist
var webDist embed.FS

const (
	StatusQueued   = "QUEUED"
	StatusRunning  = "RUNNING"
	StatusSuccess  = "SUCCESS"
	StatusFailed   = "FAILED"
	StatusCanceled = "CANCELED"
	StatusAborted  = "ABORTED"

	defaultListenAddress = ":28088"
	defaultMaxHistory    = 5000
	taskRunWaitParam     = "wait"
	configReloadInterval = time.Second
	defaultScriptHeader  = "#!/usr/bin/env bash"

	displayTimeLayout = "06-01-02 15:04:05"

	sampleConfig = `server:
  addresses:
    - "127.0.0.1:28088"
  log_dir: "logs"
  max_history: 5000
  script_header: |
    #!/usr/bin/env bash

tasks:
  - id: "hello"
    name: "Hello world"
    description: "Print a greeting"
    script: "echo hello $BUILDA_INPUT_NAME"
    timeout: "30s"
    inputs:
      - id: "name"
        name: "Name"
        type: "string"
        default: "world"
`

	configHelp = `
Configuration guide

Builda reads a YAML config file. If --config is omitted, Builda creates this
file when it does not exist:

  %s

Complete config.yaml example:

  server:
    # Keep Builda on loopback for local use. Binding to ":28088" or
    # "0.0.0.0:28088" exposes it broadly and is only appropriate on a trusted
    # private network with additional security controls.
    addresses:
      - "127.0.0.1:28088"

    # Relative paths are resolved from the directory containing config.yaml.
    log_dir: "logs"

    # Maximum number of completed run history entries to retain.
    max_history: 5000

    # Header prepended to every task script. Use this to configure shell and
    # platform-specific startup such as PATH, Homebrew, Git LFS, or direnv.
    script_header: |
      #!/usr/bin/env bash

    # Set this to enable and protect the Web UI config editor.
    # config_password: "change-me"

  tasks:
    - id: "hello"
      name: "Hello world"
      description: "Print a greeting with run-time inputs"
      script: "echo \"Hello ${BUILDA_INPUT_NAME} from ${BUILDA_INPUT_ENVIRONMENT}\""
      timeout: "30s"
      inputs:
        - id: "name"
          name: "Name"
          description: "Name to print"
          type: "string"
          default: "world"
          required: true
        - id: "environment"
          name: "Environment"
          description: "Deployment-style target selector"
          type: "choice"
          default: "local"
          options:
            - "local"
            - "staging"
            - "prod"

    - id: "list-files"
      name: "List files"
      description: "Show repository files"
      script: "find . -maxdepth 2 -type f | sort"
      timeout: "10s"

Field reference:

  server.address
    HTTP listen address. Default is ":28088" when both server.address and
    server.addresses are omitted. Kept for single-address configs.

  server.addresses
    Optional list of HTTP listen addresses. Use this to bind multiple specific
    interfaces, such as "127.0.0.1:28088" and "192.168.0.40:28088". The
    --addr flag may be repeated and overrides both server.address and
    server.addresses.

  server.log_dir
    Directory for run logs and persisted run state. Default is "logs".

  server.max_history
    Maximum number of completed run history entries to retain in runs.json.
    Defaults to 5000. Queued and running runs are always retained.

  server.config_password
    Optional password for the Web UI config editor and /api/config. When
    omitted or empty, the home page hides the config button and the HTTP
    config editor is disabled. CLI config get/set does not require this
    password.

  server.script_header
    Optional Bash script header prepended to every task script. Defaults to
    "#!/usr/bin/env bash". Use this for platform-specific startup such as PATH
    exports or shell profile sourcing.

  tasks[].id
    Required unique task ID. Use URL-safe IDs such as "deploy-staging".

  tasks[].name
    Optional display name. Defaults to tasks[].id.

  tasks[].description
    Optional short text shown in the Web UI.

  tasks[].script
    Required Bash script body. Builda prepends server.script_header, then runs
    the configured script. Treat every task as privileged shell execution on
    the host.

  tasks[].timeout
    Optional Go duration such as "30s", "5m", or "1h".

  tasks[].inputs
    Optional run-time inputs. Inputs must be declared before callers can pass
    them as query parameters. Values are persisted in run state, written to run
    logs, and may appear in script output; do not use inputs for secrets.

  tasks[].inputs[].id
    Required input ID. Use only letters, digits, underscores, and hyphens.
    "wait" is reserved for the task run API and cannot be used as an input ID.
    The script receives the value as BUILDA_INPUT_<ID>, with hyphens converted
    to underscores and letters uppercased. For example, "target-env" becomes
    BUILDA_INPUT_TARGET_ENV.

  tasks[].inputs[].name
    Optional display name. Defaults to the input ID.

  tasks[].inputs[].description
    Optional help text shown next to the input.

  tasks[].inputs[].type
    "string", "input", or "choice". "input" is accepted as an alias for
    "string". Omitted type defaults to "string".

  tasks[].inputs[].default
    Optional default value. For choice inputs, it must be one of options.

  tasks[].inputs[].required
    Optional boolean. When true, the run request must provide a non-empty value
    or a non-empty default.

  tasks[].inputs[].options
    Required for choice inputs and invalid for string inputs. Options must be
    non-empty and unique.

Run API examples:

  curl -X POST http://localhost:28088/api/tasks/hello/run
  curl -X POST "http://localhost:28088/api/tasks/hello/run?name=Builda&environment=local"
  curl -X POST "http://localhost:28088/api/tasks/hello/run?wait=1"

Security note:

  Builda is internal-only software and is not hardened for untrusted networks.
  Do not expose it without adding authentication, authorization, CSRF
  protection, and transport security.

Print only the minimal sample config:

  builda sample-config
`
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
