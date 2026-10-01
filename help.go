package main

// Role help text. Keep these aligned with the YAML schema so --help alone is
// enough to author a controller or agent config.
const rootHelp = `
Builda runs build jobs on remote machines from one central controller.

Roles

  builda controller serve   Owns jobs, catalogs, agents, the queue, history,
                            and logs. Serves the Web UI and the HTTP API.
  builda agent run          Connects outbound to the controller, runs one job
                            at a time, and streams logs back. Agents never
                            listen on a port.

Quick start

  # On the controller host
  builda controller sample-config > controller.yaml
  builda controller admin set-password --config controller.yaml
  builda controller agent token linux-android --config controller.yaml
  builda controller serve --config controller.yaml

  # On each agent host
  builda agent sample-config > agent.yaml
  builda agent enroll --config agent.yaml < token.txt
  builda agent run --config agent.yaml

Migration from the legacy standalone server

  builda migrate export --machine linux --config <legacy config> --out-dir bundle
  builda migrate plan --bundle bundle --out mapping.yaml
  builda migrate config --bundle bundle --map mapping.yaml --out-dir new
  builda migrate import --bundle bundle --map mapping.yaml --config controller.yaml

  Every migrate subcommand is a dry run until --apply is passed.

Security

  Builda is an internal tool for a private network and is not hardened.
  The controller requires authentication on the Web UI and on every API,
  including logs. Use HTTPS whenever traffic crosses a trust boundary.
`

const controllerHelp = `
Run and manage the Builda controller.

The controller owns jobs, shared catalogs, agent definitions, the single
central queue, run history, and logs. Agents connect to it outbound; the
controller never dials an agent.

Config file

  role: controller

  server:
    addresses: ["127.0.0.1:28080"]   # listen addresses, trusted interfaces
    state_dir: "state"               # state.json, logs/, credentials.json
    max_history: 5000                # terminal executions retained
    heartbeat_interval: "5s"
    offline_after: "30s"
    long_poll_timeout: "25s"

  catalogs:                          # reusable option lists
    - id: "projects"
      options:
        - value: "focus_stopwatch"
          label: "Focus Stopwatch"
          labels: ["android", "ios"] # platform labels used to filter options
          values:
            path: "focus_stopwatch"  # workspace-relative path, no traversal

  jobs:
    - id: "android-build"
      labels: ["linux", "android"]   # an agent must carry every label
      timeout: "45m"
      workdir_param: "project"       # run in the selected project directory
      script: |
        bash "$BUILDA_WORKSPACE/$BUILDA_PARAM_PROJECT_PATH/tools/build.sh" "$BUILDA_PARAM_ACTION"
      parameters:
        - id: "project"
          type: "choice"
          required: true
          catalog: "projects"
          catalog_labels: ["android"] # keep options carrying all these labels
        - id: "action"
          type: "choice"
          default: "debug"
          options: [{value: "debug"}, {value: "release"}]

  agents:
    - id: "linux-android"
      labels: ["linux", "android"]
      enabled: true
      paused: false

Parameters

  Types are string, choice, and boolean. Values reach the script only as
  environment variables, never by text substitution:

    BUILDA_PARAM_<ID>            the selected value
    BUILDA_PARAM_<ID>_LABEL      the display label of the selected option
    BUILDA_PARAM_<ID>_<FIELD>    one entry of the option's values map

  Two parameters that normalize to the same variable name are rejected, and
  inherited BUILDA_* variables are dropped before the script runs.

Credentials

  Admin password verifiers and tokens live in state_dir/credentials.json with
  mode 0600. They never appear in the config file or in any API response.

    builda controller admin set-password
    builda controller agent token <agent-id>
    builda controller token create <name>
`

const agentHelp = `
Run and manage a Builda agent.

The agent connects outbound to the controller over authenticated HTTP long
polling, runs one job at a time, journals every transition durably, and
streams logs to disk before uploading them.

Config file

  role: agent

  agent:
    id: "linux-android"                      # must match a controller agent id
    controller_url: "http://10.0.0.5:28080"
    spool_dir: "spool"                       # journal, local logs, token
    workspace_root: "/home/you/git/dooroo"   # every path parameter resolves here
    heartbeat_interval: "5s"
    poll_timeout: "30s"
    script_header: |
      #!/usr/bin/env bash
      set -euo pipefail
      source "$HOME/.config/builda/env.sh"

Everything host-local stays host-local: the workspace root, the shell header,
tool paths, and credential environment are never sent to the controller.

Enrollment

  builda controller agent token linux-android   # on the controller host
  builda agent enroll --token-file token.txt    # on the agent host

Recovery

  An agent restart never re-executes an incomplete run. The agent proves the
  previous process group ended and reports ABORTED, or escalates the execution
  for operator attention instead of guessing or killing an unrelated process.
`

const migrateHelp = `
Move a legacy standalone installation onto the controller and agent roles.

Every subcommand reports what it would do and changes nothing until --apply
is passed. The legacy installation is only ever read.

  1. On each legacy machine, stop its service, back up its state, then export:

       builda service stop
       cp -a ~/.config/builda ~/builda-backup-$(date +%Y%m%d)
       builda migrate export --machine linux \
         --config ~/.config/builda/config.yaml --out-dir ./bundle-linux --apply

     Export reads config.yaml and logs/runs.json as data. It never builds a
     runner, so no queued legacy run can start while you migrate.

  2. Draft a mapping and review it by hand:

       builda migrate plan --bundle ./bundle-linux \
         --agent-id linux-android --workspace-root "$HOME/git/dooroo" \
         --out ./bundle-linux/mapping.yaml --apply

     The mapping records which job each legacy task becomes, which catalog
     project it selects, and how legacy input values map onto new parameter
     values. iOS "adhoc" and "deploy" map to "ad-hoc" and "app-store".

  3. Generate the new role configs. Several machines can be merged at once so a
     project that exists on both platforms becomes one catalog entry carrying
     both platform labels:

       builda migrate config --bundle ./bundle-linux --map ./bundle-linux/mapping.yaml \
         --bundle ./bundle-mm --map ./bundle-mm/mapping.yaml --out-dir ./new --apply

  4. Import history into the controller. Run this while the controller is
     stopped: the state snapshot has a single writer, so importing into a
     running controller would race it.

       builda controller service stop
       builda migrate import --bundle ./bundle-linux \
         --map ./bundle-linux/mapping.yaml --config ./new/controller.yaml --apply

     Imported executions get new IDs and store the original task snapshot,
     name, inputs, and timeout. Log files are copied under the new execution
     ID, derived from the legacy run ID alone rather than from any path inside
     the bundle. A missing log is reported as a diagnostic and the run is
     still imported without one. Only terminal legacy runs are imported, so an
     import can never queue work.

     An import is all-or-nothing: every log is copied and flushed before any
     state is committed, and a failure removes what that attempt created, so
     retrying after fixing the cause simply works.

     Each imported legacy run is recorded in a durable ledger keyed by machine
     and legacy run ID. Repeating an import is a no-op even after the run it
     produced has been pruned by the history cap or deleted by an operator;
     such a run is reported as already imported and is not recreated. An
     import that does not fit under server.max_history is refused with the
     value to raise it to, rather than pruning live history to make room.

  5. Install and start the new services, then verify before retiring the old
     one:

       builda controller service install --binary "$(command -v builda)"
       builda agent service install --binary "$(command -v builda)"
       builda controller service status

Rollback

  The legacy config, logs, and runs.json are left untouched, and the backup
  from step 1 is a second copy. To roll back, stop the new services and start
  the legacy one again:

       builda controller service stop
       builda agent service stop
       builda service start
`
