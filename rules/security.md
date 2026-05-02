# Security Rules

- Builda is internal-only software. Treat every deployment as trusted-local or trusted-private-network only.
- Add a clear disclaimer whenever documenting exposure or operation: the project is not hardened and security risks are expected to exist across the implementation.
- Builda runs configured commands with `sh -c`; treat every configured task as privileged shell execution on the host.
- Do not expose a running Builda server to untrusted networks without adding authentication, authorization, CSRF protection, and transport security.
- The config editor can change commands. Treat `/config` and `/api/config` as administrative surfaces.
- The task run API must start only existing configured tasks. Never add an endpoint that accepts raw commands from request bodies, query strings, or headers.
- Keep the default sample address on loopback-style local operation. If documenting `0.0.0.0`, include an explicit warning about trusted-network use only.
- The `--addr` flag may be repeated to bind specific interfaces. Document that `--addr` overrides `server.address`, and warn that `:PORT` or `0.0.0.0:PORT` binds broadly.
- Do not commit secrets, tokens, private keys, local `.env` files, run logs, or command output.
- Run `gitleaks detect --source . --no-banner --redact --verbose` before claiming the repository is safe to publish.
- If a future task needs credentials, document environment-variable names but commit only `.env.example` with placeholder values.
