# Builda

Builda runs build jobs on remote machines from one central controller.

- The **controller** owns jobs, reusable catalogs, agent definitions, one central queue, run history, and logs. It serves the Web UI and the HTTP API.
- An **agent** connects outbound to the controller, runs one job at a time, and streams logs back. Agents never listen on a port, so no inbound firewall rule is needed on a build machine.

A single binary provides both roles plus the migration tooling. The Web UI is embedded, so `go install` needs no Node toolchain.

## Security Posture

> DISCLAIMER: Builda is an internal tool for a private, trusted network. It is not a hardened product, and security issues are expected to exist across the implementation. Do not expose it to the public internet.

The controller requires authentication on the Web UI and on every API, including run logs. There is no unauthenticated job, config, or log surface. A run request can only name a configured job and set its declared parameters; no run endpoint accepts a script. Scripts are written by an authenticated admin through the job and config endpoints, which is what configuring a job means.

What is implemented:

- A single admin account whose password is stored as a salted PBKDF2-HMAC-SHA256 verifier, with per-client login rate limiting.
- Browser sessions in an HttpOnly, SameSite cookie, marked Secure over HTTPS, with a CSRF token required on every browser state change plus a same-origin check.
- API bearer tokens for external automation, and per-agent tokens that only reach the agent API and only for their own agent identity.
- Secrets are generated randomly, shown once, and stored only as verifiers in a separate credential file with mode `0600`. No API response ever returns a verifier.

Per-execution logs are capped by `server.max_log_bytes` (64 MiB by default) so an agent that never stops writing cannot fill the controller's disk; a capped log says so in its own text and the agent keeps its full local copy.

What is not implemented: transport security of its own, authorization roles, audit logging, tenant isolation, and the rest of a production security program. **Put the controller behind HTTPS whenever traffic crosses a trust boundary**, for example a reverse proxy terminating TLS on the same host. Binding to `:28080` or `0.0.0.0:28080` exposes the controller on every interface; use those only on a network you fully trust.

Job scripts are privileged shell execution on the agent host. Treat every job, every catalog path, and the agent shell header as such.

## Install

```bash
go install github.com/BizDooroo/builda@latest
```

Prebuilt binaries for Linux and macOS on `amd64` and `arm64` are published on the [GitHub Releases page](https://github.com/BizDooroo/builda/releases). Windows is not published because jobs run through `/usr/bin/env bash`.

## Quick start

On the controller host:

```bash
builda controller sample-config > controller.yaml
builda controller admin set-password --config controller.yaml
builda controller agent token linux-android --config controller.yaml   # prints the token once
builda controller serve --config controller.yaml
```

The admin credential can only be created locally with the CLI. Until it exists, no one can sign in from outside.

On each agent host:

```bash
builda agent sample-config > agent.yaml
# edit agent.id, controller_url, workspace_root, and script_header
builda agent enroll --config agent.yaml --token-file token.txt
builda agent run --config agent.yaml
```

## Jobs, catalogs, and parameters

A job declares the labels an agent must carry, a timeout, a script, and its parameters. A parameter is `string`, `choice`, or `boolean`. A choice parameter takes either inline options or a shared catalog filtered by requiring every listed label, so one `projects` catalog can feed both an Android job and an iOS job and a new project appears in both at once.

Parameters reach the script only as environment variables, never by text substitution, so a value cannot introduce shell expansion:

| Variable | Meaning |
| --- | --- |
| `BUILDA_PARAM_<ID>` | the selected value |
| `BUILDA_PARAM_<ID>_LABEL` | the display label of the selected option |
| `BUILDA_PARAM_<ID>_<FIELD>` | one entry of the selected option's `values` map |
| `BUILDA_WORKSPACE` | the agent workspace root |
| `BUILDA_JOB_ID`, `BUILDA_JOB_NAME`, `BUILDA_EXECUTION_ID`, `BUILDA_AGENT_ID` | execution identity |

Two parameters that normalize to the same variable name are rejected when the config is validated, and inherited `BUILDA_*` variables are dropped before the script runs so a parent process cannot spoof a value. A `path` entry on an option is resolved under the agent workspace root; absolute paths, `..` segments, and symlinks that leave the root are refused.

Run `builda controller --help` for the full config reference.

## Queue and scheduling

There is one central queue. Each free, online, enabled, unpaused agent whose labels cover the job receives the earliest runnable item; an item that nothing can run does not stall the items behind it. Among eligible agents the one with the oldest last assignment wins, breaking ties by agent ID.

An agent runs one job at a time. Different agents run in parallel, and the **same project may build on two agents at once** — this is deliberate, there is no project lock.

The queue page explains why each waiting item is waiting: no matching labels, offline, paused, disabled, or busy.

## Execution lifecycle

```
QUEUED -> ASSIGNED -> RUNNING -> SUCCESS | FAILED | CANCELED | ABORTED
                         └─ CANCELING while a running cancellation is outstanding
```

- An agent journals an accepted assignment durably, then asks the controller for a start permit. Granting the permit is serialized against cancellation, so a duplicate message can never start a second process.
- Cancelling a queued execution is immediate. Cancelling an assigned one revokes the permit, which proves the script never started. Cancelling a running one kills the whole process group, and the execution is only marked `CANCELED` once the agent confirms. If the agent is offline the cancellation stays durably pending: the slot is not released and no completion is invented.
- A job that exceeds its timeout is reported as `FAILED` with the failure reason `timeout`.
- Losing the connection never stops a build. Heartbeats run on their own goroutine while a script executes, uploads, or polls.

## Recovery

- **Controller restart.** Queues and assignments are restored from the snapshot. Running work is not aborted. Every agent counts as offline until it polls again, so nothing is reassigned on a stale view.
- **Agent shutdown.** Stopping an agent does not wait for a long build. It stops supervising, reports nothing it cannot know, and leaves the script running in its own process group for the next start to reconcile.
- **Agent restart.** An incomplete run is never re-executed. The agent proves the process group it started has ended and reports `ABORTED`, or, if ownership or termination cannot be proven, it blocks that execution for operator attention instead of killing a process it cannot prove it owns or accepting more work. Resolve such a run from the UI or with `POST /api/runs/{id}/resolve`.
- **Logs.** An agent writes its log to disk first and uploads it at byte offsets. Duplicate chunks are idempotent, a gap is refused with the durable offset so the agent retransmits, and a result is only confirmed once the controller holds every log byte. The agent keeps its local log and result until the controller acknowledges both.
- **Persistence.** Run and assignment state live in one JSON snapshot written atomically with mode `0600`. Every mutation fails closed: if the snapshot cannot be written, the enqueue, assignment, or start is not acknowledged.

## Web UI

Sign in at `/login`. The screens are jobs, queue, history, catalogs, agents, and settings. Management uses dedicated forms rather than a raw YAML box; the validated YAML editor remains on the settings page. Logs follow live, stay selectable while polling, and copy works outside a secure context. The UI ships Korean and English and a light/dark/system theme toggle.

## API

All endpoints require a session cookie or an `Authorization: Bearer` API token. Browser state changes additionally require the `X-Builda-CSRF` header.

| Method and path | Purpose |
| --- | --- |
| `GET/POST /api/jobs`, `GET/PUT/DELETE /api/jobs/{id}` | job CRUD |
| `POST /api/jobs/{id}/runs` | enqueue a run from declared parameters |
| `GET/POST /api/catalogs`, `GET/PUT/DELETE /api/catalogs/{id}` | catalog CRUD |
| `GET/POST /api/agents`, `GET/PUT/DELETE /api/agents/{id}` | agent CRUD |
| `POST/DELETE /api/agents/{id}/token` | issue, rotate, or revoke an agent token |
| `GET /api/queue`, `POST /api/queue/cancel` | queue inspection and batch cancel |
| `GET /api/runs` | history, filtered by `job`, `project`, `agent`, `status`, paginated with `limit` and `offset` |
| `GET /api/runs/{id}`, `GET /api/runs/{id}/log` | run detail and a bounded log window |
| `POST /api/runs/{id}/cancel`, `/rerun`, `/resolve`, `DELETE /api/runs/{id}` | run actions |
| `GET/POST /api/config`, `GET/POST /api/tokens`, `DELETE /api/tokens/{id}` | administration |
| `POST /api/agent/v1/{poll,permit,heartbeat,log,result,attention}` | agent protocol |

`wait=1` on a run request blocks until the execution finishes and returns the run with its log. Disconnecting never cancels it.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  -X POST "http://controller:28080/api/jobs/android-build/runs?project=focus_stopwatch&action=release&wait=1"
```

## Migrating from the standalone role

`builda serve` is the deprecated standalone role, kept so an existing installation can keep running while its history moves across. It no longer serves a Web UI; its JSON API stays available.

> **The standalone role has no authentication.** Anyone who can reach its address can start any configured task, which means running a shell script as the service user. That has always been true of it, and it is left unchanged so a running installation keeps working until it is migrated. Keep it on loopback or a trusted private network, migrate it, and then stop it. Only `/api/config` is protected, by the plain `server.config_password`, and that is disabled by default. None of the controller's authentication applies to this role.

Every `builda migrate` subcommand reports what it would do and changes nothing until `--apply` is passed, and the legacy installation is only ever read. Run `migrate import` against a stopped controller: the state snapshot has a single writer.

An import is all-or-nothing, and each imported legacy run is recorded in a durable ledger keyed by machine and legacy run ID, so repeating an import stays a no-op even after the history cap or an operator has removed the run it produced. See `builda migrate --help` for the export, plan, config, and import steps, and for the rollback procedure.

## Running as a user daemon

```bash
builda controller service install --binary "$(command -v builda)"
builda agent service install --binary "$(command -v builda)"
builda controller service status
builda controller service diagnose      # reports health and repair commands, changes nothing
```

Linux installs a systemd user unit; macOS installs a launchd LaunchAgent. The service runs the Builda executable directly — no shell, wrapper, or login session — and install refuses a target that is not a stable executable regular file or whose role config does not parse. On macOS the agent is pinned to the desktop login session, so install and start must run from a terminal on that Mac rather than over SSH.

Service files pin an absolute binary path. After `go install` or unpacking a new release, reinstall with `--force --binary "$(command -v builda)"` and restart.

## Development

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
make fmt lint test build
go test -race ./...
```

Rebuild `web/dist` after changing anything under `web/src/` and commit the result with the source change; CI compares a fresh build against the committed assets.

## Release

Releases are tag-driven:

```bash
git tag -a v0.1.0 -m "builda v0.1.0"
git push origin v0.1.0
```

Before publishing, run `go test ./...`, `git diff --check`, and `gitleaks detect --source . --no-banner --redact --verbose`.
