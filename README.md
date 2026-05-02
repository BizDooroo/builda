# Builda Task Runner

A small Go web server that runs preconfigured shell commands from `config.yaml` and saves each run as a log file.

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
    command: "echo hello"
    timeout: "30s"
```

The first screen is split into a task list and a run list. Starting a task appends a run to the queue; Builda executes one run at a time and starts the next queued run after the active run finishes. Run state is persisted in `logs/runs.json`, and any run found in `RUNNING` state after a restart is marked `ABORTED`.

Use **View log** in the run list to open the run log page. Logs refresh while a run is queued or running, and each run records request, start, finish, and cancellation times.

Each configured task can also be started through its own web API endpoint:

```bash
curl -X POST http://localhost:8080/api/tasks/hello/run
```
