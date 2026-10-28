# Reach your home lab from anywhere with one command, without port forwarding

## No port forwarding, no DMZ, no firewall rules: your server dials out, and a small CLI does the rest.

*[Image: hero. A terminal showing `ccx add homelab1`, then `ssh me@homelab1.internal` connecting from a café.]*

There's a box under my desk. It runs Ubuntu, a few containers, a media server and whatever I'm experimenting with that week. At home it's one `ssh` away. Away from home, it used to be a small project every time.

You probably know the options:

- **Port forwarding** on the ISP router, which exposes SSH to every scanner on the internet.
- **A DMZ host**, which is port forwarding with fewer guard rails.
- **Dynamic DNS**, because the home IP address changes whenever the router reboots.
- **CGNAT**, which makes all of the above impossible anyway, because your ISP never gave you a public address.

What I wanted was boring: type one command on my laptop, paste one line on the server, and from then on reach it by name from anywhere. So I built a CLI that does exactly that. It's called **ccx**.

> **Disclosure.** I work at OpenVPN. ccx is a personal side project: it is not an OpenVPN product, OpenVPN does not support it, and the opinions here are my own. It's open source under Apache-2.0.

---

## Portals are getting smarter. Developers still live in the terminal.

Connectivity products, CloudConnexa among them, keep getting better at administration: polished portals, guided onboarding, AI assistants, and now MCP connectors so your AI agent can configure the network for you. That's genuinely useful. Most of the people who run networks don't want to memorise an API, and a good portal saves them hours.

But when I'm the one who wants to reach a server, I'm already in a terminal. I don't want to click through five pages, copy a script, wait for a prompt and paste a token. And I don't want to explain to a chatbot what I could have typed in four words.

Nothing beats the efficiency of a good CLI. If you've used Tailscale, you know the feeling: `tailscale up` and you're done. Cloudflare Tunnel and ngrok do something similar for publishing web services. CloudConnexa already has the hard parts: a global network of regions, OpenVPN tunnels, private DNS and access policies. What it didn't have was a two-step, terminal-first way to add a server. ccx is that, built entirely on the public CloudConnexa API.

---

## How it works

The trick is that **nothing connects in to your home network**. Everything dials out.

- **The server** runs a small OpenVPN connector that makes an outbound connection to the nearest CloudConnexa region.
- **Your laptop** connects to CloudConnexa with the OpenVPN Connect app, also outbound.
- **CloudConnexa** routes traffic between the two and gives the server a private DNS name such as `homelab1.internal`.

Outbound connections work through almost any home router, CGNAT included. There's nothing to forward, no DMZ, no dynamic DNS and no inbound firewall rule. The server has no open port on the internet.

*[Image: diagram. Laptop (OpenVPN Connect) → outbound tunnel → CloudConnexa region ← outbound tunnel ← home server (connector, behind the ISP router). A dotted line from laptop to server labelled `ssh me@homelab1.internal`.]*

ccx automates the setup. When you run `ccx add homelab1`, it calls the CloudConnexa API v1 to:

1. Create a **host** named `homelab1` with the domain `homelab1.internal`.
2. Create its **connector** in your default region.
3. Create an **IP service** for it, used by access groups if you ever switch from the default Full Mesh topology to Custom.
4. Generate a **one-time enrolment token** and print the line to paste on the server.

A few design choices matter here:

- **Your API secret never leaves your laptop.** It lives in the macOS Keychain (or the Windows Credential Manager or Linux Secret Service), never in a file or a log.
- **The server only sees the one-time token.** Generating a new token invalidates the old one.
- **No leftovers.** Every connector uses a CloudConnexa seat, so if any step fails after the host is created, ccx deletes it again.
- **It only touches its own hosts.** ccx tags what it creates and refuses to modify anything else unless you pass `--force`.

---

## From a MacBook to a private Ubuntu box: step by step

Here's the whole setup: a MacBook, and an Ubuntu 24.04 server called `homelab1` on my home network.

### 1. Create an API credential (once)

ccx needs a CloudConnexa account on the **Essential plan or higher**, or the free 14-day trial. (More on that at the end.)

In the CloudConnexa Admin Portal, go to **API & Logs → API → Create Credentials**. Pick a lifetime (one year is sensible for a personal laptop) and grant only these four permissions:

- Host – Modify
- Connector Profile – Modify
- Region – Read Only
- Settings – Read Only

Copy the client ID and client secret. The secret is shown only once.

### 2. Connect your laptop (once)

Install **OpenVPN Connect** from openvpn.net/client, import your profile from `https://<your-cloud-id>.openvpn.com`, sign in and switch the connection on. This is your normal user login, separate from the API credential. It's what makes names like `homelab1.internal` resolve on your laptop.

### 3. Install ccx and log in (once)

```bash
brew install rkalbag/tap/ccx
ccx login
```

`ccx login` asks for your Cloud ID (the `acme` in `acme.openvpn.com`), the client ID and secret, the credential lifetime, a domain suffix (default `internal`) and a default region. It then checks the credential against the API:

```text
Logged in to acme.api.openvpn.com
  client ID:      ccx-laptop.acme
  default region: <region-id> (<city>, <country>)
  domain suffix:  .internal
  credential:     1 year, expires around 28 Oct 2027 (estimated from today)
  secret:         stored in the OS keychain
Next: ccx add <name>
```

> **macOS says "Apple could not verify ccx is free of malware"?** The binaries aren't signed by Apple yet. Click **Done** (not Move to Trash), then run `xattr -d com.apple.quarantine "$(brew --prefix)/bin/ccx"` once. Homebrew prints the same hint after installing.

### 4. Add the server

```bash
ccx add homelab1
```

```text
Run this on homelab1 (a new token invalidates this one):
  curl -fsSL https://raw.githubusercontent.com/rkalbag/openvpn-cloudconnexa-cli/v0.1.0/scripts/install.sh | sudo bash -s -- <TOKEN>
Once online: `ccx status homelab1`, then `ssh user@homelab1.internal`
```

Treat that line like a password until it has been used.

### 5. Paste the line on the server

Sitting at home, SSH into the box over your LAN (or use its keyboard) and paste the line. It adds OpenVPN's package repository, installs the connector, enrols it with the token and starts it as a systemd service, so it survives reboots. There are no prompts.

```text
==> Adding the OpenVPN apt repository (noble)
==> Installing python3-openvpn-connector-setup
==> Enrolling this host with CloudConnexa
==> Done. Check from your laptop: ccx status <name> --wait 2m
```

> **Tip:** if the server is on your LAN and your user has passwordless sudo, skip the paste: `ccx add homelab1 --push me@192.168.1.50` installs it over SSH for you.

### 6. Wait for it to come online

```bash
ccx status homelab1 --wait 2m
```

```text
homelab1: ONLINE
  domain:      homelab1.internal
  tunnel IPv4: 100.96.1.10
  region:      <region-id>
```

### 7. SSH by name, from anywhere

Take the laptop to a café, a hotel or the office. With OpenVPN Connect switched on:

```bash
ssh me@homelab1.internal
```

Or let ccx look up the name for you. If DNS isn't resolving, it falls back to the tunnel IP:

```bash
ccx ssh me@homelab1
```

That's it. The router config is untouched, no port is forwarded, and the server is reachable only through your CloudConnexa network.

*[Image: GIF or asciinema of steps 4–7, about 30 seconds.]*

### Day two

- `ccx ls` lists your servers and whether they're online.
- `ccx sleep homelab1` suspends the connector and frees its seat; `ccx wake homelab1` brings it back.
- `ccx token homelab1` prints a fresh install line if you rebuild the box.
- `ccx rm homelab1` deletes it completely.
- `ccx doctor` checks your setup and warns before the API credential expires.

Under the default Full Mesh topology, every connected user in your CloudConnexa account can reach every port on the server. If you share the account, switch to Custom topology and use access groups to narrow that down.

ccx supports Ubuntu, Debian, Raspberry Pi OS (64-bit), RHEL, AlmaLinux, Rocky Linux and Fedora as servers, on x86_64 and ARM64. Ubuntu 24.04 is the one I've tested on real hardware so far; reports for the others are very welcome.

---

## The catch: the API isn't on the free plan

ccx is built entirely on the CloudConnexa Public API, and **the API is available only on paid plans (Essential and higher) and during the free trial**. The free Starter plan has no API access, so once a trial ends on Starter, ccx can't add or remove servers.

For developers and home labbers, that's the gap. A free plan with API access would make ccx a genuinely free way to reach a home lab from anywhere.

**If you'd use ccx on the free plan, here's how to help make the case:**

1. **Upvote the GitHub issue.** Open [github.com/rkalbag/openvpn-cloudconnexa-cli/issues/1](https://github.com/rkalbag/openvpn-cloudconnexa-cli/issues/1) and click the 👍 reaction on the issue itself. Reactions are what get counted, so please don't post a "+1" comment.
2. **Say what you'd use it for.** If you have a minute, add one comment to the issue: what you'd connect (a home lab, a Raspberry Pi, a dev VM) and how many servers. Concrete use cases make a much stronger case than numbers alone.
3. **Clap for this article** and leave a response saying you'd like the API on the free plan. Medium responses are public, so they're easy to point to.

I can't promise an outcome, but clear demand is the best argument there is.

---

## Try it

- **Repo, docs and issues:** github.com/rkalbag/openvpn-cloudconnexa-cli
- **Install:** `brew install rkalbag/tap/ccx` on macOS, or download a release archive for Linux.
- **Get access:** start a CloudConnexa trial or use an Essential account, then `ccx login`.

ccx is at version 0.1, so expect rough edges. Bug reports, distro test results and pull requests are all welcome. And if it saves you from forwarding one more port, I'd love to hear about it.
