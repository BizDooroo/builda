# Deployment Rules

- When the user asks to deploy, inspect the worktree, run required checks, commit the intended changes, push the current branch, create the next patch SemVer tag unless told otherwise, and push the tag to upstream.
- Derive the next default release tag from the highest existing `vMAJOR.MINOR.PATCH` tag by incrementing `PATCH`.
- Push to the configured upstream remote for the branch. If no upstream exists, use `origin` and set upstream on push.
- Do not include unrelated, generated, secret-bearing, log, coverage, or local binary files in deployment commits.
- Before tagging a deployment, inspect GitHub workflow steps **and `.goreleaser.yaml`** and keep every hard-coded path aligned with the current file layout. The release job globs archive files by name, so a moved or renamed file fails the release after the tag is already public, and only a new tag can fix it.
- If checks fail, do not create or push a release tag until the failure is fixed or the user explicitly accepts the risk.

## User daemons
- Install controller and agent services separately; each role has its own unit name, launchd label, config path, and log files.
- The service target is always the Builda executable itself. Never install a shell wrapper, and validate that the target is an executable regular file outside a temporary directory or the Go build cache, and that its role config parses.
- A launchd job with `RunAtLoad` is already started by `bootstrap`. Never follow bootstrap with `kickstart -k`; that starts a second instance while the first still holds its socket and produces a restart loop. Implement restart as a verified unload followed by a verified load, not as a kill.
- Run `launchctl enable` before `bootstrap` so a persistent per-user `disabled` override cannot leave the job loaded but never allowed to run.
- `bootout` returns before the job is gone. Follow every teardown with a probe that waits for the label to disappear, and follow every load with a probe that proves the loaded job references the plist just written. Without those, a swallowed error reports a successful install while launchd keeps running the old binary.
- Treat "already loaded" and "already gone" launchd responses as success only where they are genuinely expected. Never classify a permission or System Integrity refusal as "already gone"; that is a real failure and must surface.
- `ProcessType` picks a scheduling tier and a job's children inherit it. Use `Background` only for a service that does no heavy work; a build agent needs `Adaptive` or it throttles every compiler it starts.
- Validate the service target and its role config before writing anything, and create a config on demand only for the default path. An explicit `--config` that does not exist is a mistake, not a request for a sample tree.
- launchd user agents live in the GUI domain and need an active desktop login session. Install and start from a terminal on that Mac rather than over SSH.
- Service files pin an absolute binary path. After installing a new binary, reinstall with `--force --binary "$(command -v builda)"` and restart before judging the served Web UI.
- Use `builda <role> service diagnose` to report service file, domain, target, config health, and any persistent override without changing anything.
- On macOS, a successful request from an SSH shell does not prove a LaunchAgent can reach the controller: SSH command-line tools are exempt from Local Network privacy. Allow Builda under System Settings > Privacy & Security > Local Network, then verify authenticated heartbeats and a test execution from the actual LaunchAgent. See [Apple TN3179](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy).
- A running agent is not proof that its build workspace is ready. Check the configured project directories and required tools in the agent's execution environment before enabling build jobs; keep jobs disabled while their source directories are missing.
