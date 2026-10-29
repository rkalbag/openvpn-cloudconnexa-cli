# ccx

**Remote connectivity using CloudConnexa made simpler with a CLI.** Add a Linux server with one command on your laptop and one paste on the server, then reach it by name.

> **Unofficial project.** ccx is an independent open-source tool. It is not made, endorsed or supported by OpenVPN Inc. OpenVPN and CloudConnexa are registered trademarks of OpenVPN Inc.; their use here only describes compatibility. Support is community, best effort.

## Requirements
- A CloudConnexa account on the **Essential plan or higher** (the free Starter plan has no Public API), or a 14-day trial.
- API credentials created in the Admin Portal (one-time, below).
- Your laptop connected to CloudConnexa with OpenVPN Connect. ccx runs on macOS and Linux (Intel or ARM) and Windows. On Linux it stores the secret through the desktop keyring (Secret Service, e.g. GNOME Keyring or KWallet), so `ccx login` does not work on a headless machine without one.
- Linux servers, x86_64 or 64-bit ARM:

  | Distribution | Versions |
  |---|---|
  | Ubuntu | 20.04, 22.04, 24.04 |
  | Debian | 11, 12, 13 |
  | Raspberry Pi OS | 64-bit only (no 32-bit packages exist) |
  | RHEL, AlmaLinux, Rocky | 8, 9 |
  | Fedora | best effort (community-maintained packages) |

  Tested on real machines so far: Ubuntu 24.04. The others follow the CloudConnexa portal's own install steps but are untested; please report what works.

## One-time setup
1. Admin Portal → **API & Logs → API → Create Credentials**.
2. Choose a lifetime (1 year recommended) and these permissions: Host – Modify, Connector Profile – Modify, Region – Read Only, Settings – Read Only.
3. Install ccx and log in:
   ```
   brew install rkalbag/tap/ccx      # macOS
   ccx login
   ```
   On Linux (or without Homebrew), download the archive for your platform from [Releases](https://github.com/rkalbag/openvpn-cloudconnexa-cli/releases) and put `ccx` on your PATH, e.g.:
   ```
   V=0.1.0 A=linux_amd64   # or linux_arm64, darwin_arm64, darwin_amd64
   curl -fsSL "https://github.com/rkalbag/openvpn-cloudconnexa-cli/releases/download/v$V/ccx_${V}_$A.tar.gz" | tar -xz ccx
   sudo install ccx /usr/local/bin/
   ```
   Your client secret is stored in your OS keychain, never on disk.

### macOS: "Apple could not verify ccx is free of malware"
The macOS binaries are not yet signed or notarised by Apple, so Gatekeeper blocks the first run of a copy installed with Homebrew or downloaded in a browser, with the popup *"ccx" Not Opened: Apple could not verify "ccx" is free of malware that may harm your Mac or compromise your privacy.* Click **Done** (not **Move to Trash**), then clear the quarantine flag once:
```
xattr -d com.apple.quarantine "$(brew --prefix)/bin/ccx"   # Homebrew
xattr -d com.apple.quarantine ./ccx                        # downloaded binary
```
If `xattr` replies `No such xattr: com.apple.quarantine`, the flag is already gone: just run `ccx` again. Instead of `xattr`, you can also choose **Open Anyway** in System Settings → Privacy & Security after the popup. Binaries fetched with `curl` are not quarantined and need neither step.

## Add a server
```
$ ccx add web1              # or: ccx add web1 ssh https  (adds ssh.web1.internal and https.web1.internal)
Run this on web1 (a new token invalidates this one):
  curl -fsSL https://raw.githubusercontent.com/rkalbag/openvpn-cloudconnexa-cli/<tag>/scripts/install.sh | sudo bash -s -- <TOKEN>

# paste that on the server, then:
$ ccx status web1 --wait 2m
$ ssh you@web1.internal
```

## Other commands
`ccx ls` · `ccx token <name>` · `ccx sleep|wake <name>` · `ccx rm <name>` · `ccx doctor` · `ccx logout`

## Notes
- Each service given to `ccx add` becomes an application named `<service>.<host domain>` that allows only that protocol, e.g. `ccx add web https` creates `https.web.internal` for port 443. Custom ports are named after the port: `tcp:5432` becomes `tcp5432.db.internal`.
- With the default Full Mesh topology, the host's own domain (`web.internal`) still reaches every port; use Custom topology and access groups to limit users to the applications.
- Each server uses one CloudConnexa seat from the moment it is added, until `ccx sleep` or `ccx rm`.
- Traffic flows through CloudConnexa regions, so your access policies and inspection apply.

## License
Apache-2.0. See `LICENSE`.
