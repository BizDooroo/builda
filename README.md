# Builda Task Runner

Builda is a small Go web server for running preconfigured shell tasks from `config.yaml`. It shows configured tasks, queues runs one at a time, records run state, and stores per-run logs.

## Security Posture

> DISCLAIMER: Builda is intended only for internal, trusted, local operation. It is not a hardened product, and security risks are intentionally not fully addressed across the codebase. Assume security issues are scattered throughout the current implementation.

The repository is safe to publish from a secret-scanning perspective as of the latest local check: `gitleaks detect --source . --no-banner --redact --verbose` reported no leaks.

Do not expose a running Builda instance to the public internet or any untrusted network. Builda intentionally executes configured commands with `sh -c`, and the Web UI includes a config editor that can change those commands. Authentication, authorization, CSRF protection, transport security, audit logging, tenant isolation, and other production security controls are absent.

Binding to `:8080` or `0.0.0.0:8080` can make Builda reachable on every network interface. Use those addresses only on machines and networks you fully trust.

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

Windows binaries are not published by default because Builda executes tasks through `sh -c`; Windows users need a POSIX-compatible shell environment.

## Run

With an installed binary:

```bash
builda
```

Open `http://localhost:8080`.

On first run, Builda creates a default config at the operating system's user config location:

- Linux: `$XDG_CONFIG_HOME/builda/config.yaml` or `~/.config/builda/config.yaml`
- macOS: `~/Library/Application Support/builda/config.yaml`

The sample config uses `server.log_dir: "logs"`, and relative log directories are resolved from the config file directory. With the default config, run logs and `runs.json` are stored under the same Builda config directory, for example `~/.config/builda/logs`.

Run with an explicit config file:

```bash
builda -config config.yaml
```

Print the installed version:

```bash
builda -version
```

Create a starter config:

```bash
builda -print-sample-config > config.yaml
```

During development from this repository, use the checked-in sample config explicitly:

```bash
go run . -config config.yaml
```

Override the configured bind address with `--addr`:

```bash
go run . -config config.yaml --addr :8080
go run . -config config.yaml --addr 127.0.0.1:8080 --addr 192.168.10.5:8080
go run . -config config.yaml --addr 0.0.0.0:8080
```

When `--addr` is provided, it overrides `server.address`. Repeat `--addr` to bind only the network interfaces you want.

## Configuration

Tasks are managed in YAML:

```yaml
server:
  address: "127.0.0.1:8080"
  log_dir: "logs"

tasks:
  - id: "hello"
    name: "Hello world"
    description: "Print a greeting"
    command: "echo hello"
    timeout: "30s"
```

Fields:

- `server.address`: HTTP listen address used when `--addr` is not provided. Keep this local or trusted-network only.
- `server.log_dir`: directory for run logs and `runs.json` state. Relative paths are resolved from the directory containing the config file.
- `tasks[].id`: stable task identifier used by the UI and API.
- `tasks[].name`: display name. Defaults to `id` when omitted.
- `tasks[].description`: optional short description shown in the task list.
- `tasks[].command`: shell command executed via `sh -c`.
- `tasks[].timeout`: optional Go duration such as `30s` or `5m`.

## Web UI

The first screen shows all configured tasks and the latest 10 runs. The task list shows each task's name, description, expand button, and run button; expanded details include the command and API address. Starting a task appends a run to the queue. Builda executes one run at a time and starts the next queued run after the active run finishes.

Open `/runs` for the full run list workspace. The run list shows request time, start time, elapsed time, and completed duration; tablet and mobile layouts switch the run list to a dropdown selector. Selecting a run shows its detail and log in the right pane. Logs refresh while a run is queued or running, and each run records request, start, finish, and cancellation times.

Open `/runs?task=hello` to show only runs for one task in the run list workspace.

The config editor is available at `/config`.

## API

Start a task by ID:

```bash
curl -X POST http://localhost:8080/api/tasks/hello/run
```

Legacy form-compatible start endpoint:

```bash
curl -X POST -d task_id=hello http://localhost:8080/api/tasks/start
```

Other useful endpoints:

- `GET /api/state`: current tasks and run summaries. Add `?task={taskID}` to return only runs for one task.
- `GET /api/runs/{runID}`: one run summary.
- `POST /api/runs/{runID}/cancel`: cancel a queued or running task.
- `GET /api/runs/{runID}/log`: run log text.
- `GET /api/config`: current YAML config.
- `POST /api/config`: save YAML config after validation.

## Persistence

Run state is persisted in `logs/runs.json`. Any run found in `RUNNING` state after a restart is marked `ABORTED`; queued runs are resumed.

`logs/` is intentionally ignored by Git because command output may contain local paths or secrets.

## Development

```bash
make fmt lint test build
```

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
