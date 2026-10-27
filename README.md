# ccx

**Tailscale-simple server access for CloudConnexa.** Add a Linux server with one command on your laptop and one paste on the server, then reach it by name.

> **Unofficial project.** ccx is an independent open-source tool. It is not made, endorsed or supported by OpenVPN Inc. OpenVPN and CloudConnexa are registered trademarks of OpenVPN Inc.; their use here only describes compatibility. Support is community, best effort.

## Requirements
- A CloudConnexa account on the **Essential plan or higher** (the free Starter plan has no Public API), or a 14-day trial.
- API credentials created in the Admin Portal (one-time, below).
- Your laptop connected to CloudConnexa with OpenVPN Connect.
- Linux servers: Ubuntu, Debian, RHEL, AlmaLinux or Rocky.

## One-time setup
1. Admin Portal → **API & Logs → API → Create Credentials**.
2. Choose a lifetime (1 year recommended) and these permissions: Host – Modify, Connector Profile – Modify, Region – Read Only, Settings – Read Only.
3. Install ccx and log in:
   ```
   brew install rkalbag/tap/ccx      # or download a release binary
   ccx login
   ```
   Your client secret is stored in your OS keychain, never on disk.

## Add a server
```
$ ccx add web1              # or: ccx add web1 ssh http https  (IP service for access groups)
Run this on web1 (a new token invalidates this one):
  curl -fsSL https://raw.githubusercontent.com/rkalbag/openvpn-cloudconnexa-cli/<tag>/scripts/install.sh | sudo bash -s -- <TOKEN>

# paste that on the server, then:
$ ccx status web1 --wait 2m
$ ssh you@web1.internal
```

## Other commands
`ccx ls` · `ccx token <name>` · `ccx sleep|wake <name>` · `ccx rm <name>` · `ccx doctor` · `ccx logout`

## Notes
- With the default Full Mesh topology, connected users can reach every port on a server; the services given to `ccx add` only restrict access through access groups with Custom topology.
- Each server uses one CloudConnexa seat from the moment it is added, until `ccx sleep` or `ccx rm`.
- Traffic flows through CloudConnexa regions, so your access policies and inspection apply.

## License
Apache-2.0. See `LICENSE`.
