# Security Rules

- Builda is internal-only software. Treat every deployment as trusted-local or trusted-private-network only.
- Add a clear disclaimer whenever documenting exposure or operation: the project is not hardened and security risks are expected to exist across the implementation.
- Builda runs configured scripts by prepending `server.script_header` to the task script; the default header is `#!/usr/bin/env bash`. Treat every configured task and script header as privileged shell execution on the host.
- Do not expose a running Builda server to untrusted networks without adding authentication, authorization, CSRF protection, and transport security.
- The config editor can change scripts. Treat `/config` and `/api/config` as administrative surfaces.
- `server.config_password` protects only the Web UI config editor and `/api/config`. CLI `builda config get/set` remains an administrator-local operation and must not require that password.
- When `server.config_password` is omitted or empty, hide the config button in the Web UI and disable HTTP config editing.
- The task run API must start only existing configured tasks. Never add an endpoint that accepts raw scripts from request bodies, query strings, or headers.
- Query parameters on task run APIs may only provide values for configured task inputs; validate choice values and reject undeclared input names before queueing.
- The task run API may also accept the reserved `wait` control query parameter; do not pass it to scripts as a task input.
- Task input values are persisted in run state, written to run logs, and may appear in script output. Do not treat task inputs as a secret transport.
- Keep the default sample address on loopback-style local operation. If documenting `0.0.0.0`, include an explicit warning about trusted-network use only.
- The `--addr` flag may be repeated to bind specific interfaces. Document that `--addr` overrides `server.address` and `server.addresses`, and warn that `:PORT` or `0.0.0.0:PORT` binds broadly.
- Do not commit secrets, tokens, private keys, local `.env` files, run logs, or script output.
- Run `gitleaks detect --source . --no-banner --redact --verbose` before claiming the repository is safe to publish.
- If a future task needs credentials, document environment-variable names but commit only `.env.example` with placeholder values.
