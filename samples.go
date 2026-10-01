package main

// Sample role configs. They contain no credentials: the admin password and
// every token live only in the protected credential files.
const sampleControllerConfig = `role: controller

server:
  # Keep the controller on a trusted interface. Use HTTPS through a reverse
  # proxy if it is reachable across a trust boundary.
  addresses:
    - "127.0.0.1:28080"
  # Relative paths resolve from the directory holding this config file.
  state_dir: "state"
  max_history: 5000
  # Per-execution log cap. A capped log says so in its own text and the agent
  # keeps its full local copy.
  max_log_bytes: 67108864
  heartbeat_interval: "5s"
  offline_after: "30s"
  long_poll_timeout: "25s"

catalogs:
  - id: "projects"
    name: "Projects"
    options:
      - value: "example_app"
        label: "Example app"
        labels: ["android", "ios"]
        values:
          path: "example_app"

jobs:
  - id: "android-build"
    name: "Android build"
    labels: ["linux", "android"]
    timeout: "45m"
    workdir_param: "project"
    script: |
      bash "$BUILDA_WORKSPACE/$BUILDA_PARAM_PROJECT_PATH/tools/build_android.sh" "$BUILDA_PARAM_ACTION"
    parameters:
      - id: "project"
        name: "Project"
        type: "choice"
        required: true
        catalog: "projects"
        catalog_labels: ["android"]
      - id: "action"
        name: "Action"
        type: "choice"
        default: "debug"
        options:
          - value: "debug"
          - value: "release"
          - value: "aab"
          - value: "bump"

  - id: "ios-build"
    name: "iOS build"
    labels: ["macos", "ios"]
    timeout: "2h"
    workdir_param: "project"
    script: |
      bash "$BUILDA_WORKSPACE/app-deploy-tools/scripts/builda_ios_entrypoint.sh" \
        "$BUILDA_WORKSPACE/$BUILDA_PARAM_PROJECT_PATH/tools/build_ios.sh" "$BUILDA_PARAM_ACTION"
    parameters:
      - id: "project"
        name: "Project"
        type: "choice"
        required: true
        catalog: "projects"
        catalog_labels: ["ios"]
      - id: "action"
        name: "Action"
        type: "choice"
        default: "ad-hoc"
        options:
          - value: "ad-hoc"
          - value: "app-store"

agents:
  - id: "linux-android"
    name: "Linux Android agent"
    labels: ["linux", "android"]
  - id: "macos-ios"
    name: "macOS iOS agent"
    labels: ["macos", "ios"]
`

const sampleAgentConfig = `role: agent

agent:
  # Must match an agent id configured on the controller.
  id: "linux-android"
  controller_url: "http://127.0.0.1:28080"
  # Journal, local logs, and the agent token live here. Relative paths resolve
  # from the directory holding this config file.
  spool_dir: "spool"
  # Every job path parameter is resolved beneath this root.
  workspace_root: "/home/you/git/dooroo"
  heartbeat_interval: "5s"
  poll_timeout: "30s"
  # Host-local shell startup. Credentials stay on this machine; never put
  # secrets in controller job scripts.
  script_header: |
    #!/usr/bin/env bash
    set -euo pipefail
    source "$HOME/.config/builda/env.sh"
`
