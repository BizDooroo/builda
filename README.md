# Builda Task Runner

Builda is a small Go web server for running preconfigured shell tasks from `config.yaml`. It shows configured tasks, queues runs one at a time, records run state, and stores per-run logs.

## Security Posture

> DISCLAIMER: Builda is intended only for internal, trusted, local operation. It is not a hardened product, and security risks are intentionally not fully addressed across the codebase. Assume security issues are scattered throughout the current implementation.

The repository is safe to publish from a secret-scanning perspective as of the latest local check: `gitleaks detect --source . --no-banner --redact --verbose` reported no leaks.

Do not expose a running Builda instance to the public internet or any untrusted network. Builda intentionally executes configured commands with `sh -c`, and the Web UI includes a config editor that can change those commands. Authentication, authorization, CSRF protection, transport security, audit logging, tenant isolation, and other production security controls are absent.

## Run

```bash
go run . -config config.yaml
```

Open `http://localhost:8080`.

## Configuration

Tasks are managed in YAML:

```yaml
server:
  address: ":8080"
  log_dir: "logs"

tasks:
  - id: "hello"
    name: "Hello world"
    description: "Print a greeting"
    command: "echo hello"
    timeout: "30s"
```

Fields:

- `server.address`: HTTP listen address. Keep this local or trusted-network only.
- `server.log_dir`: directory for run logs and `runs.json` state.
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
gofmt -w main.go main_test.go
go test ./...
git diff --check
gitleaks detect --source . --no-banner --redact --verbose
```
