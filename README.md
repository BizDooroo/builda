# Builda Task Runner

Builda is a small Go web server for running preconfigured shell tasks from `config.yaml`. It shows configured tasks, queues runs one at a time, records run state, and stores per-run logs.

## Security Posture

> DISCLAIMER: Builda is intended only for internal, trusted, local operation. It is not a hardened product, and security risks are intentionally not fully addressed across the codebase. Assume security issues are scattered throughout the current implementation.

The repository is safe to publish from a secret-scanning perspective as of the latest local check: `gitleaks detect --source . --no-banner --redact --verbose` reported no leaks.

Do not expose a running Builda instance to the public internet or any untrusted network. Builda intentionally executes configured scripts as Bash scripts, and the Web UI can include a config editor that changes those scripts. Authentication, authorization, CSRF protection, transport security, audit logging, tenant isolation, and other production security controls are absent.

Binding to `:28088` or `0.0.0.0:28088` can make Builda reachable on every network interface. Use those addresses only on machines and networks you fully trust.

## Install

Install the latest tagged version with Go:

```bash
go install github.com/BizDooroo/builda@latest
```

Install a specific version:

```bash
go install github.com/BizDooroo/builda@v0.1.0
```

Prebuilt binaries are published on the [GitHub Releases page](https://github.com/BizDooroo/builda/releases) for Linux and macOS on `amd64` and `arm64`. Download the archive for your platform, unpack it, and run the `builda` binary.

Windows binaries are not published by default because Builda executes tasks through `/usr/bin/env bash`; Windows users need a POSIX-compatible shell environment with Bash.

## Run

With an installed binary:

```bash
builda
```

Open `http://localhost:28088`.

On first run, Builda creates a default config at the operating system's user config location:

- Linux: `$XDG_CONFIG_HOME/builda/config.yaml` or `~/.config/builda/config.yaml`
- macOS: `~/Library/Application Support/builda/config.yaml`

The sample config uses `server.log_dir: "logs"`, and relative log directories are resolved from the config file directory. With the default config, run logs and `runs.json` are stored under the same Builda config directory, for example `~/.config/builda/logs`.

Run with an explicit config file:

```bash
builda --config config.yaml
```

Print the installed version:

```bash
builda version
```

Create a starter config:

```bash
builda sample-config > config.yaml
```

Print the active config path:

```bash
builda config path
builda --config config.yaml config path
```

Print or replace the active config file:

```bash
builda config get
builda --config config.yaml config set new-config.yaml
cat new-config.yaml | builda --config config.yaml config set
```

`builda config set` validates the YAML before writing. Invalid config input is rejected without replacing the current config file. CLI config commands are intended for administrators and do not require the Web UI config password. A running Builda server reloads the changed config file automatically, so task changes become available without restarting the daemon.

During development from this repository, use the checked-in sample config explicitly:

```bash
go run . --config config.yaml
```

Override the configured bind address with `--addr`:

```bash
go run . --config config.yaml --addr :28088
go run . --config config.yaml --addr 127.0.0.1:28088 --addr 192.168.10.5:28088
go run . --config config.yaml --addr 0.0.0.0:28088
```

When `--addr` is provided, it overrides `server.address` and `server.addresses`. Repeat `--addr` to bind only the network interfaces you want.

## Daemon Install

Builda can install itself as a user-level daemon. The install command creates the default config when needed, writes the service definition, enables it, and starts it by default.

Ubuntu 24.04 and other systemd Linux distributions use a systemd user unit:

```bash
builda service install
systemctl --user status builda.service
```

The unit is written to `$XDG_CONFIG_HOME/systemd/user/builda.service` or `~/.config/systemd/user/builda.service`. User services usually start after the user logs in. To allow the service to start at boot before login, enable linger outside Builda:

```bash
sudo loginctl enable-linger "$USER"
```

macOS uses a launchd LaunchAgent:

```bash
builda service install
launchctl print "gui/$(id -u)/com.bizdooroo.builda"
```

The plist is written to `~/Library/LaunchAgents/com.bizdooroo.builda.plist`.

Useful service commands:

```bash
builda --config /path/to/config.yaml service install --force
builda --config /path/to/config.yaml --addr 127.0.0.1:28088 service install
builda service install --dry-run
builda service print --target linux
builda service print --target darwin
builda service status
builda service restart
builda service stop
builda service start
builda service uninstall
```

Use `--binary /path/to/builda` when installing from a temporary working directory and you want the daemon to keep using a stable binary path. Use `--start=false` to write and enable the service without starting it immediately.

Do not install a daemon that binds to `:PORT` or `0.0.0.0:PORT` unless the machine and network are trusted and protected. Builda is internal-only software and is not hardened for untrusted access.

## Configuration

Tasks are managed in YAML:

```yaml
server:
  addresses:
    - "127.0.0.1:28088"
  log_dir: "logs"
  script_header: |
    #!/usr/bin/env bash
  # Set this to enable and protect the Web UI config editor.
  # config_password: "change-me"

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
      - id: "environment"
        name: "Environment"
        type: "choice"
        default: "local"
        options:
          - "local"
          - "staging"
          - "prod"
```

Fields:

- `server.address`: single HTTP listen address used when `--addr` is not provided. Keep this local or trusted-network only.
- `server.addresses`: optional list of HTTP listen addresses used when `--addr` is not provided. Use this to bind multiple specific interfaces, such as `127.0.0.1:28088` and `192.168.0.40:28088`.
- `server.log_dir`: directory for run logs and `runs.json` state. Relative paths are resolved from the directory containing the config file.
- `server.config_password`: optional password for the Web UI config editor and `/api/config`. When omitted or empty, the home page hides the config button and the HTTP config editor is disabled.
- `server.script_header`: optional Bash script header prepended to every task script. Defaults to `#!/usr/bin/env bash`. Use this for platform-specific startup such as `PATH` exports or shell profile sourcing.
- `tasks[].id`: stable task identifier used by the UI and API.
- `tasks[].name`: display name. Defaults to `id` when omitted.
- `tasks[].description`: optional short description shown in the task list.
- `tasks[].script`: Bash script body. Builda prepends `server.script_header`, then runs the configured script.
- `tasks[].timeout`: optional Go duration such as `30s` or `5m`.
- `tasks[].inputs`: optional run-time inputs. Each input has an `id`, optional `name` and `description`, `type` (`string`, `input`, or `choice`), optional `default`, optional `required`, and `options` for `choice`.

Input IDs become environment variables for the script as `BUILDA_INPUT_{ID}` with hyphens converted to underscores and letters uppercased. For example, `id: "target-env"` is available as `$BUILDA_INPUT_TARGET_ENV`. `wait` is reserved for the task run API and cannot be used as an input ID. Run inputs are stored with run state, so do not use them for secrets.

## Web UI

The first screen shows all configured tasks and the latest 10 runs. The task list shows each task's name, description, expand button, and run button; expanded details include the script, configured inputs, and API address. If a task has inputs, pressing Run opens a popup for string fields and choice selectors before appending the run to the queue. Builda executes one run at a time and starts the next queued run after the active run finishes.

Open `/runs` for the full run list workspace. The run list shows request time, start time, elapsed time, and completed duration; tablet and mobile layouts switch the run list to a dropdown selector. Selecting a run shows its detail and log in the right pane. Logs refresh while a run is queued or running, and each run records request, start, parameters, finish, and cancellation times.

Open `/runs?task=hello` to show only runs for one task in the run list workspace.

When `server.config_password` is set, the config editor is available at `/config` and requires that password before loading or saving YAML. When the password is omitted, the home page does not show the config button and the HTTP config editor is disabled.

The Web UI source is an Astro static frontend in `web/`. Builda embeds only the built `web/dist/` files into the Go binary. The built dist files are committed so `go install github.com/BizDooroo/builda@latest` can build the single binary without requiring Node or pnpm.

## API

Start a task by ID:

```bash
curl -X POST http://localhost:28088/api/tasks/hello/run
```

Pass configured inputs as query parameters:

```bash
curl -X POST "http://localhost:28088/api/tasks/hello/run?name=Builda&environment=local"
```

Wait for a run to finish and return its summary plus log:

```bash
curl -X POST "http://localhost:28088/api/tasks/hello/run?wait=1"
```

Legacy form-compatible start endpoint:

```bash
curl -X POST -d task_id=hello http://localhost:28088/api/tasks/start
```

Other useful endpoints:

- `GET /api/meta`: server metadata for the static Web UI, including hostname, log directory, start time, config path, and whether config editing is enabled.
- `GET /api/state`: current tasks and run summaries. Add `?task={taskID}` to return only runs for one task.
- `GET /api/runs/{runID}`: one run summary.
- `POST /api/runs/{runID}/cancel`: cancel a queued or running task.
- `GET /api/runs/{runID}/log`: run log text.
- `GET /api/config`: current YAML config, only when the Web UI config password is provided.
- `POST /api/config`: save YAML config after validation, only when the Web UI config password is provided.

## Persistence

Run state is persisted in `logs/runs.json`. Any run found in `RUNNING` state after a restart is marked `ABORTED`; queued runs are resumed.

`logs/` is intentionally ignored by Git because script output may contain local paths or secrets.

## Development

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
make fmt lint test build
```

Run `pnpm --dir web build` after changing files under `web/src/` and commit the resulting `web/dist/` changes with the source changes.

## Release

Releases are tag-driven. Push a semantic version tag to build and publish GitHub Release assets:

```bash
git tag -a v0.1.0 -m "builda v0.1.0"
git push origin v0.1.0
```

The release workflow runs tests, builds Linux and macOS archives with GoReleaser, uploads `checksums.txt`, and generates GitHub artifact attestations for the release artifacts.

Before publishing a release, run:

```bash
go test ./...
git diff --check
gitleaks detect --source . --no-banner --redact --verbose
```
