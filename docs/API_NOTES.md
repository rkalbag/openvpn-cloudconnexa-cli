# API notes — CloudConnexa Public API v1

Sources: `api/v1.json` (project copy of the OpenAPI spec), the Admin Portal of a test account (21 Oct 2026), the public `OpenVPN/openvpn-connector-setup` source, and the CloudConnexa pricing page. Field names below come from v1.json; when in doubt, v1.json wins. Items marked **VERIFY** were not confirmed.

## Base URL and auth
- Base URL: `https://<cloud-id>.api.openvpn.com` (format assumed from docs; v1.json `servers` is just `/`). Spec version: OpenAPI 3.1.0, API 1.2.4.
- Token: `POST /api/v1/oauth/token?client_id=<id>&client_secret=<secret>&grant_type=client_credentials`
  - Response `OAuthResponse`: `access_token`, `token_type` (`bearer`), `expires_in` (~43199 s ≈ 12 h), `scope` (`read write`).
  - v1.json documents credentials **only as query parameters** (`client_id`, `client_secret`, `grant_type`, all `in: query`; no `requestBody`). ccx sends them form-encoded in the body (keeps the secret out of URL logs) and falls back to query params. **Settled live 27 Oct 2026:** the form body is accepted on the first try (`ccx --debug doctor` logs `oauth token accepted mode="form body"`); the fallback stays for safety.
  - v1.json patterns: `client_id` `^[A-Za-z0-9.-]*$`, `client_secret` `^[A-Za-z0-9]*$`. `ccx login` can reject malformed input locally (without echoing it).
  - `expires_in` is typed `string` in `OAuthResponse` but the example is the number `43199`. Decode both a JSON number and a numeric string.
  - Cache the token in memory; refresh ~5 min before expiry. Never persist it.
- Header: `Authorization: Bearer <token>`.
- Retries (ccx policy): 429 is retried for every method; 5xx and network errors only for GET/PUT/DELETE, never for POST (a failed create may still have created the host).
- Rate limits: undocumented. Some 429s say "retry after 1 minute". Back off exponentially; honour `Retry-After` (v1.json does not declare a `Retry-After` header anywhere).

## Errors (from v1.json)
- None of the Phase 1 endpoints declare error responses; only success codes. 401/429 are declared (empty body, `*/*`) on a handful of other endpoints (DNS log, access visibility, Cyber Shield); **403 is declared nowhere**.
- Where declared, 400/500 bodies are `ErrorResponse`: `errorMessage` (string), `statusError` (string), `errorDetails` (map of field → `[ErrorDetails{errorCode, message, params}]`). Parse it best-effort; fall back to the status code if the body is empty or not JSON.
- No schema in v1.json has a `required` list, so every response field is optional: validate presence (e.g. host `id`, connector `id`) before use.
- Plan: Public API requires **Essential, Premium or Enterprise** (not Starter). Expect 401/403 otherwise.
- Credentials (portal): lifetime 2 weeks / 1 month / 6 months / 1 year; per-resource **Read Only / Modify** permissions; optional allowed IPs. Credentials work only with API v1. Expiry date is not exposed by the API.

## Pagination
- List endpoints take `page` (from 0) and `size` (1–1000, default 10). (v1.json declares their schema type as `"int32"`, which is malformed OpenAPI; generators may choke — another reason for hand-written types.) Responses: `content[]`, `totalElements`, `totalPages`, `numberOfElements`, `page`, `size`. Always read all pages.

## Endpoints used in Phase 1

| Purpose | Method + path | Notes |
|---|---|---|
| Regions | `GET /api/v1/regions` | Array of `VpnRegionResponse`: `id`, `continent`, `country`, `countryIso`, `regionName` |
| Topology | `GET /api/v1/settings/wpc/topology` | **Confirmed in v1.json** (`getTopology`). 200 body is a bare `string`, content type `*/*`; values `FULL_MESH` \| `CUSTOM` (enum from the `PUT`). Accept both raw text and a JSON-quoted string. Needs Settings – Read Only |
| Create host + connector | `POST /api/v1/hosts` | Body `HostCreateRequest`: `name`, `description`, `internetAccess` (`SPLIT_TUNNEL_ON` \| `SPLIT_TUNNEL_OFF` \| `RESTRICTED_INTERNET`), `domain`, `connectors[]` (`HostConnectorRequest`: `name`, `description`, `vpnRegionId`), `gatewaysIds[]`. Returns `HostResponse` (201) |
| List hosts | `GET /api/v1/hosts?page=&size=` | `PageHostResponse` |
| Get / update / delete host | `GET` / `PUT` / `DELETE /api/v1/hosts/{id}` | Delete returns 204 and removes connectors, applications, IP services; revokes connector profiles |
| Get connector | `GET /api/v1/hosts/connectors/{id}` | `HostConnectorResponse`; cheaper than listing for `ccx status` |
| List connectors | `GET /api/v1/hosts/connectors?hostId=&page=&size=` | `HostConnectorResponse`: `id`, `networkItemId`, `networkItemType`, `name`, `description`, `vpnRegionId`, `ipV4Address`, `ipV6Address`, `connectionStatus` (`ONLINE`/`OFFLINE`), `licensed`. There is **no suspended/active field**; whether `licensed=false` means suspended is **VERIFY live** |
| Profile token | `POST /api/v1/hosts/connectors/{id}/profile/encrypt` | No request body. 200 body is a bare `string`, content type `*/*`: accept raw text or a JSON-quoted string. A new token invalidates the previous one. ccx rejects tokens outside `[A-Za-z0-9._~+/=:-]` (16–4096 chars) because they are pasted into a shell command: **Settled live 27 Oct 2026:** real tokens fit (`ccx add` accepted one and the server enrolled with it) |
| .ovpn profile (fallback) | `POST /api/v1/hosts/connectors/{id}/profile` | Returns the .ovpn as a string. `DELETE` on the same path revokes it |
| Suspend / activate | `PUT /api/v1/hosts/connectors/{id}/suspend`, `/activate` | No body either way; 200 with empty body. Suspended connectors free their seat |
| IP services | `POST /api/v1/hosts/ip-services?hostId=` (`hostId` required) | Body `HostIpServiceRequest`: `name`, `description`, `config`. Returns `HostIpServiceResponse` (201). See "IP service config" below |
| Host applications (later) | `POST /api/v1/hosts/applications?hostId=` | Domain routes: `routes[{value, allowEmbeddedIp}]`, `config` |

`HostResponse`: `id`, `name`, `description`, `internetAccess`, `domain`, `connectors[]` (full `HostConnectorResponse` objects), `systemSubnets[]`, `gatewaysIds[]`. `POST /hosts` returns 201; `DELETE /hosts/{id}` returns 204.

### IP service config (`ServiceConfigRequest`)
- `serviceTypes[]`: enum `ANY`, `CUSTOM`, `BGP`, `DHCP`, `DNS`, `FTP`, `HTTP`, `HTTPS`, `IMAP`, `IMAPS`, `NTP`, `POP3`, `POP3S`, `RDP`, `SMTP`, `SMTPS`, `SNMP`, `SSH`, `TELNET`, `TFTP`.
- `customServiceTypes[]` (`CustomServiceTypeRequest`): `protocol` (`TCP` \| `UDP` \| `ICMP`), `port[]` and `icmpType[]` of `RangeInteger` (`value`, or `lowerValue` + `upperValue`).
- "Allow all traffic" = `{"serviceTypes": ["ANY"]}`. `--service tcp:22` = `{"serviceTypes": ["CUSTOM"], "customServiceTypes": [{"protocol": "TCP", "port": [{"value": 22}]}]}` (whether `CUSTOM` must be listed alongside `customServiceTypes` is **VERIFY live**).
- Named types (`SSH`, `HTTP`, `HTTPS`, …) are assumed to mean their well-known ports (22, 80, 443, …); v1.json does not list the ports. **Settled live for `SSH`** (27 Oct 2026: `ccx add web1 ssh` allowed SSH on 22); others still **VERIFY**.
- Default when `ccx add` gets no services: `{"serviceTypes": ["ANY"]}` (decided 23 Oct 2026; open item 3 still to be checked live).

## Names
- Setting `domain` on the host at creation makes it reachable by that name for connected users (CloudConnexa DNS). No separate DNS record is needed.
- Default suffix is `internal` (e.g. `web1.internal`), decided 23 Oct 2026. `.cloud` was dropped because it is a public TLD and a ccx name could shadow a real public site while connected; `.internal` is reserved for private use. The suffix stays configurable. Settled live 27 Oct 2026: the API accepts a two-label name like `web1.internal`.

## Seats and limits (Essential)
- A seat = an active user or a connector. A connector uses a seat **from creation**, online or not, until suspended or deleted.
- Essential limits: 500 connectors, 200 domain applications, 200 IP services, 10 access groups, 3 devices per user. Starter: 2 connectors, 1 device per user, no API.

## Server bootstrap (from the portal's Ubuntu 24.04 script, version 1.0.6)
The portal shows:
```
curl -O https://network-gateway.openvpn.com/network-gate/api/v1/scripts/ubuntu-24-04/host/ubuntu-24-04.sh
chmod +x ubuntu-24-04.sh && ./ubuntu-24-04.sh
```
The script does:
```
sudo apt update && sudo apt install -y curl
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://packages.openvpn.net/packages-repo.gpg | sudo tee /etc/apt/keyrings/openvpn.asc
echo "deb [signed-by=/etc/apt/keyrings/openvpn.asc] https://packages.openvpn.net/openvpn3/debian $(lsb_release -c -s) main" | sudo tee /etc/apt/sources.list.d/openvpn-packages.list
sudo apt update
sudo apt install -y python3-openvpn-connector-setup
sudo CLOUDCONNEXA_BASEURL='https://network-gateway.openvpn.com/network-gate/api/v1/profiles/' openvpn-connector-setup
```
- `openvpn-connector-setup` accepts `--token TOKEN_VALUE` (non-interactive), plus `--name`, `--force`, `--mode` (`systemd-unit` default, or `autoload`), `--no-start`, `--dco`, `--version`.
- Token format: decryption key + 40-char file reference. The tool downloads the encrypted profile from `CLOUDCONNEXA_BASEURL + fileref`, decrypts it, imports it into openvpn3 and starts a systemd unit.
- The package's built-in default base URL is `network-management-gw.openvpn.com`, but the portal sets `network-gateway.openvpn.com`. Always set `CLOUDCONNEXA_BASEURL` like the portal does.
- Other distros: **verified 2026-10-24** by fetching `https://network-gateway.openvpn.com/network-gate/api/v1/scripts/<slug>/host/<slug>.sh` (all `VERSION 1.0.6`). Slugs: `ubuntu-20-04`, `ubuntu-22-04`, `ubuntu-24-04`, `debian`, `raspberry`, `rhel-8`, `rhel-9`, `alma-8`, `alma-9`, `rocky-8`, `rocky-9`, `fedora`. Differences from the Ubuntu script:
  - Ubuntu 20.04/22.04/24.04: identical.
  - `debian`, `raspberry`: same apt steps, plus `policykit-1` with `curl` (Debian 13 replaced `policykit-1` with `polkitd`; install.sh falls back to it).
  - `rhel-8`: `yum localinstall -y https://dl.fedoraproject.org/pub/epel/epel-release-latest-8.noarch.rpm`, `subscription-manager repos --enable codeready-builder-for-rhel-8-x86_64-rpms` (hard-coded x86_64; install.sh uses `uname -m`), then `yum install -y https://packages.openvpn.net/openvpn-openvpn3-epel-repo-1-1.noarch.rpm` and `python3-openvpn-connector-setup`.
  - `rhel-9`: the same without CodeReady Builder, EPEL 9.
  - `rocky-8` = `alma-8` (byte-identical): `yum install -y epel-release`, `yum config-manager --set-enabled powertools`, then the openvpn3 EPEL repo RPM and the package.
  - `rocky-9` = `alma-9`: `epel-release` only, then the same.
  - `fedora`: `yum install -y yum-plugin-copr`, `yum copr enable dsommers/openvpn3` (prompts without `-y`), then the package. install.sh installs `dnf-command(copr)` instead, which works with dnf4 and dnf5.
  - Every script ends with the same interactive `openvpn-connector-setup` call; install.sh adds `--token`.

## Open items to verify early
1. ~~Token endpoint accepts credentials in a form body.~~ **Settled live 27 Oct 2026:** yes, accepted on the first try.
2. ~~`GET` topology endpoint exists.~~ **Settled from v1.json:** yes, `GET /api/v1/settings/wpc/topology` → string `FULL_MESH` \| `CUSTOM`.
3. Whether a host with no IP service is reachable under Full Mesh.
4. ~~Portal and API accept `web1.internal` as a host domain.~~ **Settled live 27 Oct 2026:** accepted, and it resolves on a laptop connected with OpenVPN Connect (`ssh root@web1.internal` worked).
5. Profile token lifetime (undocumented; tokens are single-active).
6. ~~Whether an "Enable API" toggle must be switched on after creating credentials.~~ **Settled live 27 Oct 2026:** the API page shows "Enable API to activate authentication credentials", and it is switched ON automatically when the first credential is created. No action needed; if API calls return 401 with valid credentials, check that it is still on.
