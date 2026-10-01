# Security Rules

- Builda is internal-only software for a private network. Treat every deployment as trusted-private-network only, and state in any exposure documentation that the project is not hardened.
- Job scripts and the agent `script_header` are privileged shell execution on the agent host. Only admin-configured jobs run; never add an endpoint that accepts a script from a request body, query string, or header.
- The Web UI and every controller API, including run logs, require authentication. There must be no unauthenticated job, config, or log surface. Only the login page, the login call, and static page assets may be anonymous.
- The admin credential is created locally with `builda controller admin set-password` before anyone can sign in from outside. Store it as a salted PBKDF2-HMAC-SHA256 verifier and rate limit failed logins per client.
- Browser sessions use an HttpOnly, SameSite cookie, marked Secure over HTTPS. Require a CSRF token and a same-origin check on every browser state change. Bearer-token callers do not use CSRF.
- Generate tokens randomly, return the raw secret exactly once, and persist only a verifier. Support rotation and revocation. Per-agent tokens reach only the agent API and only for their own agent identity.
- Keep credentials in a protected file separate from config, with mode `0600`. Never return a verifier, salt, or hash from any API, and never write one into a config document.
- Parameter values are persisted in run state, written to run logs, and may appear in script output. Do not treat a parameter as a secret transport; keep credentials in the agent environment, which never leaves the agent host.
- Keep the sample listen address on loopback. Document that `0.0.0.0` or a bare `:PORT` exposes the controller broadly, and require HTTPS whenever traffic crosses a trust boundary.
- Do not commit secrets, tokens, private keys, local `.env` files, run logs, agent spool contents, or script output. Run `gitleaks detect --source . --no-banner --redact --verbose` before claiming the repository is safe to publish.
