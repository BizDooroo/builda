# Web UI remaining work

The UI implementation is deployed as v0.1.20. The remaining follow-ups are:

- [ ] Verify representative screens at actual 200% browser zoom. The 720×450 viewport check covered reflow but did not test browser zoom.
- [ ] Complete a screen-reader and assistive-technology accessibility review, then address any issues found.
- [ ] Make the run log pane use the document's scroll instead of its own independent scroll area.
- [ ] Revisit the iOS agent after restoring its Mac mini workspace: verify source and tool readiness, run a synthetic LaunchAgent job, then consider enabling `ios-build`. The workspace is currently empty and the job remains disabled.
