# Build plan — Phase 1

Work top to bottom. Each step ends with tests passing and this file updated.

## 0. Repo setup
- [x] `go mod init github.com/<org>/ccx`; add cobra, go-keyring, go-toml/v2 (module is `github.com/org/ccx` until the org is known: `<` is not legal in a module path; replace `github.com/org/` repo-wide. Go directive is 1.26 because current x/sys and x/term need it)
- [x] `LICENSE` (Apache-2.0 standard text), `NOTICE` ("ccx — Copyright <year> <author>"), README from `README.md` draft
- [x] Copy `api/v1.json` into the repo (from the user's project files)
- [x] CI (GitHub Actions): build 5 targets, `go test ./...` on ubuntu/macos/windows, golangci-lint (plus shellcheck + bats)
- [x] goreleaser config (archives + Homebrew tap + checksums); `scripts/install.sh` shipped as a release asset too (uses `homebrew_casks`; `brews` is deprecated)

## 1. API client (`internal/api`)
- [x] Config struct: base URL from Cloud ID; injectable `http.Client` and base URL for tests
- [x] OAuth token fetch + in-memory cache + refresh before expiry; try form body, fall back to query params (log which worked at debug level, never the values)
- [x] Error mapping: 401, 403, 404, 409, 429, 5xx → typed errors with user-facing hints
- [x] Retry with exponential backoff + jitter on 429/5xx (max 5), honour `Retry-After` (5xx and network errors are retried only for GET/PUT/DELETE: a 5xx on `POST /hosts` may already have created the host, and a blind retry could leave a seat-consuming duplicate)
- [x] Generic paginator (`page` from 0, `size` 1000)
- [x] Methods: `Regions`, `Topology`, `CreateHost`, `ListHosts`, `GetHost`, `DeleteHost`, `ListConnectors`, `ProfileToken`, `SuspendConnector`, `ActivateConnector`, `CreateIPService`
- [x] `httptest` fixtures shaped from v1.json for every method, including 429 and 401 cases
- [x] Redaction test: secret and token never appear in error strings or debug logs

## 2. Config + keychain
- [x] Config read/write per OS path; file mode 0600
- [x] Keychain wrapper with a fake for tests
- [x] Credential age tracking for `doctor` (estimated from the login time; the API does not expose expiry)

## 3. Commands
- [ ] `login` (interactive + flags + `--secret-stdin`), validates via token + regions, topology warning
- [ ] `add` with rollback on failure; `--service`, `--domain`, `--region`, `--push`
- [ ] `ls` (`--all`, `--json`), `status` (`--wait`)
- [ ] `token`, `sleep`, `wake`, `rm` (confirm, `--yes`, `--force`)
- [ ] `ssh` (DNS first, tunnel IP fallback)
- [ ] `logout`, `version`, `doctor`

## 4. Server bootstrap (`scripts/install.sh`)
- [ ] Debian/Ubuntu path (mirrors the portal script; adds `--token`)
- [ ] Fetch the portal scripts for RHEL 8/9, AlmaLinux, Rocky, Fedora, Debian, Raspberry Pi OS; implement each repo setup
- [ ] `shellcheck` clean; bats tests for argument parsing and distro detection (no network)

## 5. Live validation (with the user, test account + disposable VM)
- [ ] User creates Essential/trial credentials with the minimum permissions; runs `ccx login` (user types the secret)
- [ ] Resolve the "Open items to verify early" list in `docs/API_NOTES.md`; update notes
- [ ] `ccx add web1` → paste on Ubuntu 24.04 VM → ONLINE → `ssh user@web1.cloud` from the laptop
- [ ] `ccx rm web1 --yes`; confirm seat freed and nothing left behind
- [ ] Record a demo run for the Phase 1 video (asciinema or screen capture)

## 6. Release v0.1.0
- [ ] README quickstart matches the real flow, with screenshots of the portal setup
- [ ] Tag, release, Homebrew tap
- [ ] macOS binaries are unsigned: decide on signing/notarisation or document the Gatekeeper quarantine workaround (goreleaser's cask post-install hook is deprecated)
