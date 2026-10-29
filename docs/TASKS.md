# Build plan — Phase 1

Work top to bottom. Each step ends with tests passing and this file updated.

## 0. Repo setup
- [x] `go mod init github.com/<org>/ccx`; add cobra, go-keyring, go-toml/v2 (module is `github.com/rkalbag/openvpn-cloudconnexa-cli`, the GitHub repo; the binary is still `ccx`. Go directive is 1.26 because current x/sys and x/term need it)
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
- [x] Methods: `Regions`, `Topology`, `CreateHost`, `ListHosts`, `GetHost`, `DeleteHost`, `ListConnectors`, `ProfileToken`, `SuspendConnector`, `ActivateConnector`, `CreateApplication` (replaced `CreateIPService` on 29 Oct 2026)
- [x] `httptest` fixtures shaped from v1.json for every method, including 429 and 401 cases
- [x] Redaction test: secret and token never appear in error strings or debug logs

## 2. Config + keychain
- [x] Config read/write per OS path; file mode 0600
- [x] Keychain wrapper with a fake for tests
- [x] Credential age tracking for `doctor` (estimated from the login time; the API does not expose expiry)

## 3. Commands
- [x] `login` (interactive + flags + `--secret-stdin`), validates via token + regions, topology warning (also `--lifetime`; the old keychain item is removed when the credential changes)
- [x] `add` with rollback on failure; `--service`, `--domain`, `--region`, `--push` (refuses duplicate names/domains; after a 5xx/timeout on `POST /hosts` it looks for and deletes a host the request may have created)
- [x] `ls` (`--all`, `--json`), `status` (`--wait`; exit code 2 when not ONLINE). `ls` and `status` show `SUSPENDED` when `licensed=false` (verified live 28 Oct 2026; `ls --json` keeps `connectionStatus` in `status` and adds `licensed`)
- [x] `token`, `sleep`, `wake`, `rm` (confirm, `--yes`, `--force`; all refuse non-ccx hosts without `--force`)
- [x] `ssh` (DNS first, tunnel IP fallback; `ccx ssh [user@]name -- args`, exit code passed through)
- [x] `logout`, `version`, `doctor`

## 4. Server bootstrap (`scripts/install.sh`)
- [x] Debian/Ubuntu path (mirrors the portal script; adds `--token`; non-interactive, package tools never read stdin so `curl | bash` is safe, all work inside `main` so a truncated download runs nothing)
- [x] Fetch the portal scripts for RHEL 8/9, AlmaLinux, Rocky, Fedora, Debian, Raspberry Pi OS; implement each repo setup (all v1.0.6; summary in API_NOTES. Only Ubuntu 24.04 will be tested live in section 5; the rest are untested on real machines)
- [x] `shellcheck` clean; bats tests for argument parsing and distro detection (no network), plus whole-script runs with stubbed package tools

## 5. Live validation (with the user, test account + disposable VM)
- [x] User creates Essential/trial credentials with the minimum permissions; runs `ccx login` (user types the secret). Done 27 Oct 2026
- [ ] Resolve the "Open items to verify early" list in `docs/API_NOTES.md`; update notes (items 1, 3, 4, 6 and the token format settled 27 Oct; API credential lifetime settled 28 Oct; 5, the connector profile token lifetime, still open and does not affect ccx today)
- [x] `ccx add web1` → paste on Ubuntu 24.04 VM → ONLINE → `ssh user@web1.internal` from the laptop. Done 27 Oct 2026 on a DigitalOcean Droplet following `docs/blog/ssh-to-a-private-digitalocean-droplet.md`, with no inbound firewall rules. Ran the install.sh from before the apt-lock fix (e0ac912), so that fix is not yet tested live
- [x] `ccx sleep`/`wake` on a throwaway host (`ccxsleeptest`, never installed): `licensed` flips false/true, repeats are idempotent, host removed after. Done 28 Oct 2026
- [x] `ccx rm web1 --yes`; confirm seat freed and nothing left behind. Done 27 Oct 2026: host gone from the portal, seat freed
- [x] Record a demo run for the Phase 1 video (asciinema or screen capture). Recorded by the user during the 27 Oct 2026 live test

## 6. Release v0.1.0
- [x] README quickstart matches the real flow; supported-server table (versions from the OpenVPN package repos, 28 Oct 2026), Linux keyring requirement and tarball install
- [ ] Screenshots of the portal setup in the README (nice to have, not blocking)
- [x] `install.sh` refuses CPUs with no OpenVPN packages (32-bit ARM) before installing anything
- [x] Release workflow (`.github/workflows/release.yml`, goreleaser on `v*` tags); `rkalbag/homebrew-tap` created and `HOMEBREW_TAP_GITHUB_TOKEN` secret added by the user (fine-grained, expires within a year: renew it)
- [x] Tag v0.1.0, release, Homebrew tap. Released 28 Oct 2026: 5 archives, checksums and install.sh on the GitHub release; cask pushed to rkalbag/homebrew-tap
- [x] macOS binaries are unsigned: documented the Gatekeeper quarantine workaround in the README and in the cask `caveats` (printed by `brew install`). Signing/notarisation needs an Apple Developer ID; revisit later

## 7. Applications instead of IP services (29 Oct 2026)
- [x] `ccx add <name> <service>...` creates one host application per service, `<service>.<domain>` (e.g. `https.web.internal`, `tcp5432.db.internal`), allowing only that protocol; no services → no applications; no IP services
- [x] Rollback if any application fails; tests for naming, configs, duplicates and partial failure
- [x] Live test 29 Oct 2026 (API_NOTES open item 7): API accepts the names, permissions suffice, application names resolve from the laptop, and each allows only its protocol even under Full Mesh
- [x] Release v0.2.0 (29 Oct 2026)
