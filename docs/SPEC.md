# ccx — Phase 1 spec (server access from a laptop)

## Goal
After a one-time portal setup, adding a Linux server takes **one command on the laptop and one paste on the server**, and the server is then reachable **by name** from any connected CloudConnexa user.

## Accepted one-time setup (manual, documented in README)
1. Sign up for CloudConnexa on **Essential or higher**, or use the 14-day trial.
2. Admin Portal → **API & Logs → API → Create Credentials**. Lifetime: 2 weeks / 1 month / 6 months / 1 year. Recommend 1 year for a laptop.
3. Permissions (minimum): **Host – Modify**, **Connector Profile – Modify**, **Region – Read Only**, **Settings – Read Only**. Optional: Allowed IPs.
4. If an "Enable API" toggle appears after creating credentials, turn it on. (VERIFY: the toggle was not visible before the first credential existed.)
5. Laptop joins CloudConnexa normally with OpenVPN Connect (user profile). ccx does not manage the laptop's VPN connection in Phase 1.
6. Install ccx and run `ccx login`.

## Platforms
- Laptop CLI: macOS (amd64, arm64), Linux (amd64, arm64), Windows (amd64).
- Servers: Ubuntu 20.04/22.04/24.04, Debian, RHEL 8/9, AlmaLinux 8/9, Rocky 8/9 (Fedora and Raspberry Pi OS: best effort).

## Config
- File: `$XDG_CONFIG_HOME/ccx/config.toml` (macOS `~/Library/Application Support/ccx/`, Windows `%AppData%\ccx\`).
- Fields: `cloud_id`, `client_id`, `region_id`, `domain_suffix` (default `internal`), `credential_created_at` (time of `ccx login`; the API does not expose it), `credential_lifetime`.
- Secret: keychain item `service=ccx`, `user=<cloud_id>/<client_id>`.

## Commands

### `ccx login`
- Prompts for Cloud ID, client ID, client secret (hidden input), the credential lifetime chosen in the portal, and domain suffix (default `internal`).
- Gets an OAuth token to validate. On 401: "credentials rejected"; on 403: "check the plan is Essential or higher and the credential's permissions".
- Lists regions (`GET /api/v1/regions`) and asks for a default (sorted by continent, then country).
- Reads WPC topology (VERIFY endpoint in v1.json; expected `GET /api/v1/settings/wpc/topology`). If not `FULL_MESH`, warn: "new hosts may not be reachable until an access group grants access".
- Stores the secret in the keychain, the rest in config. Prints a summary without the secret.
- Flags: `--cloud-id`, `--client-id`, `--suffix`, `--region`, `--secret-stdin` (scripted use).

### `ccx add <name> [service...] [--region <id>] [--domain <fqdn>] [--service <svc>]... [--push user@host]`
1. Validate `<name>`: lowercase letters, digits, hyphens, 1–63 chars.
2. Domain defaults to `<name>.<domain_suffix>`, e.g. `staging-db.internal`.
3. `POST /api/v1/hosts` with `name`, `description: "[ccx] created by ccx <version>"`, `domain`, `internetAccess: SPLIT_TUNNEL_ON`, `connectors: [{name: "<name>-connector", vpnRegionId}]`.
4. Create one IP service on the host (`POST /api/v1/hosts/ip-services?hostId=`), named `<name>-services`:
   - Services come from the positional args after `<name>` plus any `--service` flags (same syntax; commas also accepted, e.g. `ccx add web1 ssh,http,https`).
   - A named service (`ssh`, `http`, `https`, `rdp`, `dns`, … — any `serviceTypes` value from v1.json except `ANY`/`CUSTOM`, case-insensitive) goes into `serviceTypes`.
   - `tcp:<port>`, `udp:<port>`, `tcp:<lo>-<hi>` or `icmp` goes into `customServiceTypes` (with `CUSTOM` in `serviceTypes`).
   - No services given: default `serviceTypes: ["ANY"]` (all traffic to the host).
   - Example: `ccx add web1 ssh http https` → host `web1.internal`, one IP service allowing only SSH (22), HTTP (80), HTTPS (443).
5. `POST /api/v1/hosts/connectors/{connectorId}/profile/encrypt` → token string.
6. Print exactly:
   ```
   Run this on <name> (a new token invalidates this one):
     curl -fsSL https://raw.githubusercontent.com/rkalbag/openvpn-cloudconnexa-cli/<tag>/scripts/install.sh | sudo bash -s -- <TOKEN>
   ```
   `<tag>` is the release tag baked in at build time (ldflags); dev builds use `main`.
7. With `--push user@host`: run the install over SSH instead of printing it (uses the system `ssh`). The token is sent on the SSH session's stdin (`install.sh -`), never on the remote command line.
8. On any failure after step 3: delete the host (rollback) and report.
9. Print the next step: "Once online: `ccx status <name>`, then `ssh user@<domain>`".

### `ccx ls`
- Lists hosts whose description starts with `[ccx]`: name, domain, connector status (ONLINE/OFFLINE), tunnel IPv4, region.
- `--all` lists every host. `--json` for machine output.

### `ccx status <name> [--wait 120s]`
- Shows the connector's `connectionStatus`; `--wait` polls every 5 s until ONLINE or timeout.

### `ccx token <name>`
- Generates a fresh profile token for an existing ccx host (invalidates the previous one) and prints the install line again.

### `ccx sleep <name>` / `ccx wake <name>`
- Suspend / activate the connector. Explain that a suspended connector frees its seat and stops connectivity.

### `ccx rm <name> [--yes]`
- Confirms, then `DELETE /api/v1/hosts/{id}` (deletes connectors and revokes profiles). Refuses non-ccx hosts unless `--force`.

### `ccx ssh <name> [-- ssh args]`
- Execs `ssh <domain>`; if DNS does not resolve (laptop not connected), falls back to the connector's tunnel IPv4 and hints to connect OpenVPN Connect.

### `ccx logout`
- Removes the keychain item and config.

### `ccx version`, `ccx doctor`
- `doctor` checks config, keychain access, token fetch, credential age vs lifetime (warn at ≤14 days left), topology and region.

## Server bootstrap (`scripts/install.sh`)
- Usage: `sudo bash install.sh <TOKEN|-> [--force]` (also via `curl | sudo bash -s -- <TOKEN>`). `-` reads the token from stdin (used by `--push`).
- Detect distro from `/etc/os-release`; mirror the portal's own install steps for each supported distro (see API_NOTES, "Server bootstrap"), without prompts.
- Install `python3-openvpn-connector-setup` from `packages.openvpn.net`, then run
  `CLOUDCONNEXA_BASEURL='https://network-gateway.openvpn.com/network-gate/api/v1/profiles/' openvpn-connector-setup --token "$TOKEN"`.
- If a connector profile already exists, require `--force` (passed through to the tool).
- Exit non-zero with a clear message on unsupported distro, missing root, or setup failure.

## Acceptance criteria (Phase 1)
1. `ccx login` with valid Essential/trial credentials stores the secret only in the keychain; searching the config directory for the secret finds nothing.
2. `ccx add web1` creates exactly one host and one connector with description prefix `[ccx]` and domain `web1.internal`, one IP service allowing `ANY`, and prints one install line.
3. If profile-token creation fails, the host created in that run is deleted (verified with `ccx ls --all`).
4. Pasting the install line on a fresh Ubuntu 24.04 VM brings the connector ONLINE within 2 minutes with no prompts.
5. From a laptop connected with OpenVPN Connect, `ssh user@web1.internal` reaches the server.
6. `ccx rm web1 --yes` deletes the host, and the seat count drops by one.
7. No command prints or logs the client secret; the profile token appears only in `ccx add` / `ccx token` output.
8. CI builds all five targets; unit tests pass on macOS, Linux and Windows runners.
9. A 429 response is retried with backoff and succeeds once the mock server stops throttling.

## Out of scope (later phases)
- Phase 2: GitHub Action (ephemeral connector per job, deleted in a post step).
- Phase 3: Docker sidecar image. Phase 4: Kubernetes operator.
- Windows/macOS servers, laptop enrolment via the Devices API (`ccx join`), access-group management.
