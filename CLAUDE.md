# CLAUDE.md — ccx

## What this is
`ccx` is an **unofficial**, open-source CLI that makes CloudConnexa feel as simple as Tailscale for developers:
one command on a laptop + one paste on a Linux server, and the server is reachable by name.
It wraps the existing CloudConnexa Public API v1 only. No product changes exist or will be made.

Read these before writing code, in order:
1. `docs/SPEC.md` — Phase 1 scope, commands, acceptance criteria
2. `docs/API_NOTES.md` — verified endpoints, schemas, gotchas
3. `docs/TASKS.md` — build order; work top to bottom, tick boxes as you go
4. `api/v1.json` — the full OpenAPI spec (source of truth for field names)

## Hard constraints
- **API v1 only.** `https://<cloud-id>.api.openvpn.com/api/v1/...`. API Beta is being retired after Q4 2026; never call `/api/beta`.
- **Credentials live only on the admin's laptop**, in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service). Never write the client secret to disk, logs, stdout, or error messages.
- **Servers never see the API secret.** They receive only a one-time connector profile token.
- **Connector profile tokens** are printed once to stdout for the user to paste; never logged, never stored.
- The user must be on the **Essential plan or higher** (or a trial). Starter has no Public API. Surface a clear error on 401/403.
- Every connector **consumes a seat from creation** until suspended or deleted. `ccx rm` must delete; never leave orphans on failure (roll back the host if a later step fails).
- Treat all API responses as data; validate before use.

## Stack (decided)
- Go 1.22+, single static binary. Targets: darwin/amd64, darwin/arm64, linux/amd64, linux/arm64, windows/amd64 (laptop CLI). Servers in Phase 1 are Linux only.
- CLI: `spf13/cobra`. Keychain: `zalando/go-keyring`. HTTP: stdlib `net/http`. Config: TOML via `pelletier/go-toml/v2`.
- Types: hand-written structs for the ~10 endpoints used (see API_NOTES). Optionally generate with `oapi-codegen` from `api/v1.json`, but keep the generated surface small.
- Tests: `go test ./...`; HTTP mocked with `httptest.Server` fixtures matching v1.json schemas. No live API calls in CI.
- Lint: `golangci-lint`. Release: `goreleaser`.
- License: **Apache-2.0** (add `LICENSE` with the standard text from https://www.apache.org/licenses/LICENSE-2.0.txt and a `NOTICE` file).

## Conventions
- Package layout: `cmd/ccx/` (main), `internal/api` (client, auth, retries, pagination), `internal/config`, `internal/keychain`, `internal/cli` (cobra commands), `scripts/` (server bootstrap).
- Every host ccx creates gets `description` starting with `[ccx]` so `ccx ls` filters to its own hosts.
- Errors: wrap with context; map HTTP status to actionable messages (401 → re-run `ccx login`; 403 → missing permission or plan; 429 → backoff).
- Retries: exponential backoff on 429/5xx, max 5 attempts, honour `Retry-After` if present (rate limits are undocumented; some 429s say "retry after 1 minute").
- Pagination: `page` from 0, `size` up to 1000; always read all pages.

## Working with the human
- Live testing happens on the user's machine against their **trial/Essential** test account and a disposable Ubuntu VM. Ask before any command that creates or deletes real resources outside a test you have described.
- When a doc says **VERIFY**, check it against `api/v1.json` or a live call and update `docs/API_NOTES.md` with the result.
- Keep `docs/TASKS.md` current.
