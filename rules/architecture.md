# Architecture Rules

## Roles
- Builda ships one binary with two operational roles. The controller owns jobs, catalogs, agent definitions, the central queue, run history, and logs. The agent connects outbound and executes. Keep `builda serve` as the deprecated standalone role for migration only, and do not give it back a Web UI.
- Agents never listen on a port. All agent traffic is outbound HTTP long polling to `/api/agent/v1/*`. Never add a controller-to-agent dial path.
- Keep Go files focused and under 500 lines: `ctrl*.go` for controller state, scheduling, auth, and HTTP; `api_*.go` for endpoints; `agent*.go` for the agent; `migrate*.go` for migration; `cli*.go` and `service*.go` for commands and daemons.

## Config and parameters
- Controller config is one validated YAML document holding server settings, catalogs, jobs, and agent definitions. It never holds credentials.
- A choice parameter takes either inline options or a catalog, never both. A catalog filter keeps an option only when it carries every listed label, so one catalog entry can serve several jobs at once.
- Parameters reach scripts only as `BUILDA_PARAM_*` environment variables. Never interpolate a parameter value into script text. Reject two declarations that normalize to the same variable name, and strip inherited `BUILDA_*` variables before execution.
- Resolve every option `path` under the agent workspace root, rejecting absolute paths, `..` segments, and symlinks that leave the root. Validate statically in config and again on the agent before execution.
- Snapshot the job, parameters, option metadata, labels, and timeout onto the execution at enqueue. A later config edit must never rewrite history.

## Queue and scheduling
- One central queue. Consider queued items in enqueue order so a blocked item never stalls the items behind it, and among eligible agents pick the oldest last assignment, breaking ties by agent ID.
- An agent is eligible when it is online, enabled, unpaused, free, unblocked, and carries every job label.
- One job at a time per agent. Different agents run in parallel and the same project may build concurrently; there is no project lock.
- Agent liveness is in-memory only. After a controller restart every agent is offline until it polls, so nothing is scheduled on a stale view.

## State and lifecycle
- Importing history competes with live runs for the terminal history cap. Refuse an import the cap cannot hold, with the number to raise it to, rather than pruning the controller's own history.
- Imported history records what happened, not what can still be requested. A recorded choice the job no longer offers is kept with a diagnostic, because dropping it would silently lose history; re-running it is refused at enqueue time, which is where that check belongs. A parameter the job no longer declares is still a hard skip, since such history could not be filtered correctly.
- Import idempotence lives in a durable ledger in the snapshot, keyed by a machine and legacy run ID struct rather than a joined string. Deciding from surviving executions is wrong: pruning or deleting a run would make it importable again. Backfill the ledger from retained origins when loading an older snapshot.
- An import is all-or-nothing. Copy and flush every log under its new execution ID before committing any state, remove what the attempt created when either step fails, and never modify the source bundle. Deduplicate repeated origins inside one bundle deterministically.
- An import with nothing to do must not mutate the snapshot at all, because a mutation also prunes.
- Persist run and assignment state in one JSON snapshot, written atomically with mode `0600`. Apply every mutation to a clone and commit only after the write succeeds, so a persistence failure never acknowledges an enqueue, assignment, or start.
- States are `QUEUED`, `ASSIGNED`, `RUNNING`, `CANCELING`, and the terminal `SUCCESS`, `FAILED`, `CANCELED`, `ABORTED`. Bound terminal history with `server.max_history`, defaulting to 5000, and never prune a queued or active execution.
- An agent journals an accepted assignment durably before acting, then obtains a start permit. Serialize granting the permit against cancellation so a duplicate message cannot start a second process.
- Cancel a queued execution immediately. Cancel an assigned one by revoking its permit, which proves the script never started. Cancel a running one by killing the process group and only finalize once the agent confirms; while the agent is unreachable keep the cancellation pending and keep the slot held.
- Refuse to delete an agent that still owns an active or blocked execution.

## Recovery
- A controller restart must not abort running work, and must not reassign an execution whose start permit was granted. Requeue only an assignment that provably never started.
- A stopping agent must not block until a long build finishes. It stops supervising, leaves the script running in its own process group, reports nothing it does not know, and lets the next start reconcile the run.
- An agent restart must never re-execute an incomplete run. Prove the process group ended and report `ABORTED`, or block the execution for operator attention. Never kill a process whose ownership cannot be proven, and never accept new work while blocked.
- A reader draining a child's output must never stop early. Whoever stops reading leaves the child blocked on a full pipe, which deadlocks the run, so split an over-long line instead of treating it as an error.
- The start permit is idempotent, so a lost response is retried, and an agent finishes anything its journal still owns before polling for more. Abandoning an accepted assignment strands the execution: the controller no longer offers it, and nothing re-delivers it.
- A controller refusal that retrying cannot fix (the execution is gone, or this agent is not its owner) is escalated for an operator with the spool path, never retried forever and never silently dropped.
- Reconciliation is reported to an agent once, when it acts. Repeating it on every poll turns the long poll into a hot loop.
- Logs are written to the agent disk first and uploaded at byte offsets. Duplicate chunks are idempotent, a gap is refused with the durable offset, and a result is confirmed only once the controller holds every byte. The log file length is the durable record and is re-read on restart.
- A job that exceeds its timeout is reported as `FAILED` with the failure reason `timeout`.

## Web UI
- Keep semantic colour tokens consistent across the shell and management screens. Scope text-input styles away from checkboxes and give clickable list rows their own layout and foreground/background styles instead of inheriting action-button styles.
- Define scroll ownership and mobile navigation explicitly. Verify long names, selected rows, and narrow Korean/English layouts in both themes; synchronize repeater drafts before rebuilding their DOM so adding or removing an item cannot erase user input.
- Outside-click handlers should use `event.composedPath()` when an inside handler can replace clicked descendants; `event.target` may be detached before the document listener runs.
- The Web UI source lives under `web/` as an Astro static frontend. Go embeds only `web/dist/`; keep the built dist committed so `go install` needs no Node toolchain.
- Give every shared script module its own output chunk. Letting the bundler merge shared modules makes the generated export order vary between builds and breaks the CI comparison against the committed dist.
- Management screens are dedicated forms, not a raw YAML box. Keep the validated YAML editor as an escape hatch on the settings page.
- Do not replace rendered log DOM during polling unless the displayed log text or the selected run changes, and keep the log element out of the translation pass, or a selection is lost on every tick.
- Pass every rendered log control, including wrap, into each page's `LogView` instance so visible controls remain connected on both history and run detail screens.
- Send only the fields an API accepts. The config APIs reject unknown fields, so never post a list or detail view object straight back.
