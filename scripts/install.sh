#!/usr/bin/env bash
# ccx server bootstrap: installs the CloudConnexa connector and enrols this host
# with a one-time connector profile token from `ccx add` / `ccx token`.
#
# Mirrors the CloudConnexa portal's own per-distro install scripts (version
# 1.0.6, fetched from network-gateway.openvpn.com/network-gate/api/v1/scripts/
# <slug>/host/<slug>.sh), made non-interactive and given the token.
#
# Usage: sudo bash install.sh <TOKEN|-> [--force]
#   -        read the token from the first line of stdin (used by `ccx add --push`)
#   --force  replace an existing connector profile on this host
#
# Everything runs inside main(), called on the last line, so a truncated
# `curl | bash` download does nothing.
set -euo pipefail

CLOUDCONNEXA_BASEURL_DEFAULT='https://network-gateway.openvpn.com/network-gate/api/v1/profiles/'
APT_KEY_URL='https://packages.openvpn.net/packages-repo.gpg'
APT_REPO_URL='https://packages.openvpn.net/openvpn3/debian'
RPM_REPO_PKG='https://packages.openvpn.net/openvpn-openvpn3-epel-repo-1-1.noarch.rpm'
EPEL_BASE='https://dl.fedoraproject.org/pub/epel'
COPR_REPO='dsommers/openvpn3'
# Overridable for tests only.
OS_RELEASE="${OS_RELEASE:-/etc/os-release}"
APT_ETC="${APT_ETC:-/etc/apt}"

USAGE='Usage: sudo bash install.sh <TOKEN|-> [--force]'

die() {
  echo "ccx install: $*" >&2
  exit 1
}
say() { echo "==> $*" >&2; }

# parse_args sets TOKEN_ARG and FORCE.
parse_args() {
  TOKEN_ARG=""
  FORCE=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --force) FORCE="--force" ;;
      -h | --help)
        echo "$USAGE"
        exit 0
        ;;
      -) [ -z "$TOKEN_ARG" ] || die "more than one token given. $USAGE"; TOKEN_ARG="-" ;;
      -*) die "unknown option: $1. $USAGE" ;;
      *) [ -z "$TOKEN_ARG" ] || die "more than one token given. $USAGE"; TOKEN_ARG="$1" ;;
    esac
    shift
  done
  [ -n "$TOKEN_ARG" ] || die "missing token. $USAGE"
}

# read_token resolves TOKEN_ARG ("-" means stdin) into TOKEN and validates it.
read_token() {
  if [ "$TOKEN_ARG" = "-" ]; then
    IFS= read -r TOKEN || [ -n "${TOKEN:-}" ] || die "no token on stdin"
  else
    TOKEN="$TOKEN_ARG"
  fi
  TOKEN="${TOKEN%$'\r'}"
  validate_token "$TOKEN"
}

# validate_token checks the shape ccx itself accepts (never prints the token).
validate_token() {
  local t="$1"
  if [ "${#t}" -lt 16 ] || [ "${#t}" -gt 4096 ] || ! [[ "$t" =~ ^[A-Za-z0-9._~+/=:-]+$ ]]; then
    die "the token does not look like a connector profile token; copy the whole line from 'ccx add' or 'ccx token' again"
  fi
}

# detect_family prints the install recipe for this distro, based on the
# portal's script for it: ubuntu, debian (also Raspberry Pi OS), rhel8, rhel9,
# el8 (Rocky/Alma/CentOS 8), el9 (Rocky/Alma/CentOS 9), fedora.
detect_family() {
  [ -r "$OS_RELEASE" ] || die "cannot detect the distribution ($OS_RELEASE missing)"
  local ID="" ID_LIKE="" VERSION_ID=""
  # shellcheck disable=SC1090
  . "$OS_RELEASE"
  local major="${VERSION_ID%%.*}"
  case "$ID" in
    ubuntu) echo ubuntu ;;
    debian | raspbian) echo debian ;;
    fedora) echo fedora ;;
    rhel) el_family rhel "$major" ;;
    rocky | almalinux | centos) el_family el "$major" ;;
    *)
      case " $ID_LIKE " in
        *" ubuntu "*) echo ubuntu ;;
        *" debian "*) echo debian ;;
        *" rhel "* | *" centos "*) el_family el "$major" ;;
        *) die "unsupported distribution: ${ID:-unknown} (supported: Ubuntu, Debian, Raspberry Pi OS, RHEL, Rocky, AlmaLinux 8/9, Fedora)" ;;
      esac
      ;;
  esac
}

el_family() {
  case "$2" in
    8 | 9) echo "$1$2" ;;
    *) die "unsupported version: $1 ${2:-unknown} (8 and 9 are supported)" ;;
  esac
}

# apt_codename prints the release codename for the OpenVPN apt repo.
apt_codename() {
  local UBUNTU_CODENAME="" VERSION_CODENAME=""
  # shellcheck disable=SC1090
  . "$OS_RELEASE"
  # Ubuntu derivatives (e.g. Mint) carry the Ubuntu codename separately.
  local c="${UBUNTU_CODENAME:-$VERSION_CODENAME}"
  if [ -z "$c" ] && command -v lsb_release >/dev/null; then
    c="$(lsb_release -cs)"
  fi
  [ -n "$c" ] || die "cannot detect the release codename"
  echo "$c"
}

# Package commands never read stdin: under `curl | bash` stdin is this script.
# On a freshly booted server, unattended-upgrades holds the apt locks for a
# while: installs wait for the dpkg lock, and updates are retried.
APT_LOCK_TIMEOUT=300
APT_UPDATE_TRIES=6
APT_UPDATE_DELAY=10
apt_get() {
  DEBIAN_FRONTEND=noninteractive apt-get -y -q -o "DPkg::Lock::Timeout=$APT_LOCK_TIMEOUT" "$@" </dev/null
}
apt_update() {
  local i
  for ((i = 1; i <= APT_UPDATE_TRIES; i++)); do
    apt_get update && return 0
    [ "$i" -lt "$APT_UPDATE_TRIES" ] || break
    say "apt is busy (first-boot updates?); retrying in ${APT_UPDATE_DELAY}s"
    sleep "$APT_UPDATE_DELAY"
  done
  return 1
}
dnf_() {
  if command -v dnf >/dev/null; then
    dnf -y "$@" </dev/null
  else
    yum -y "$@" </dev/null
  fi
}

install_apt() {
  local family="$1" codename
  codename="$(apt_codename)"
  say "Adding the OpenVPN apt repository ($codename)"
  apt_update || die "apt update failed; check this server's network and package sources"
  apt_get install curl ca-certificates
  if [ "$family" = debian ]; then
    # The portal installs policykit-1; Debian 13 replaced it with polkitd.
    apt_get install policykit-1 || apt_get install polkitd
  fi
  mkdir -p "$APT_ETC/keyrings" "$APT_ETC/sources.list.d"
  curl -fsSL "$APT_KEY_URL" -o "$APT_ETC/keyrings/openvpn.asc"
  echo "deb [signed-by=/etc/apt/keyrings/openvpn.asc] $APT_REPO_URL $codename main" \
    >"$APT_ETC/sources.list.d/openvpn-packages.list"
  apt_update || die "apt update failed; the OpenVPN repository may not support '$codename' yet"
  say "Installing python3-openvpn-connector-setup"
  apt_get install python3-openvpn-connector-setup
}

install_rpm() {
  local family="$1"
  say "Adding the EPEL and OpenVPN repositories"
  case "$family" in
    rhel8 | rhel9)
      dnf_ install "$EPEL_BASE/epel-release-latest-${family#rhel}.noarch.rpm"
      if [ "$family" = rhel8 ]; then
        subscription-manager repos --enable "codeready-builder-for-rhel-8-$(uname -m)-rpms" </dev/null
      fi
      ;;
    el8)
      dnf_ install epel-release dnf-plugins-core
      dnf_ config-manager --set-enabled powertools
      ;;
    el9) dnf_ install epel-release ;;
    fedora)
      dnf_ install 'dnf-command(copr)'
      dnf_ copr enable "$COPR_REPO"
      ;;
  esac
  if [ "$family" != fedora ]; then
    dnf_ install "$RPM_REPO_PKG"
  fi
  say "Installing python3-openvpn-connector-setup"
  dnf_ install python3-openvpn-connector-setup
}

main() {
  parse_args "$@"
  [ "$(id -u)" -eq 0 ] || die "must run as root (use sudo)"
  read_token
  local family
  family="$(detect_family)"
  case "$family" in
    ubuntu | debian) install_apt "$family" ;;
    *) install_rpm "$family" ;;
  esac

  say "Enrolling this host with CloudConnexa"
  # The token is single-use; the setup tool only accepts it as an argument.
  CLOUDCONNEXA_BASEURL="${CLOUDCONNEXA_BASEURL:-$CLOUDCONNEXA_BASEURL_DEFAULT}" \
    openvpn-connector-setup --token "$TOKEN" ${FORCE:+"$FORCE"} </dev/null ||
    die "openvpn-connector-setup failed. If this host was enrolled before, re-run with --force; if the token was used or replaced, run 'ccx token <name>' on your laptop for a new one."

  say "Done. Check from your laptop: ccx status <name> --wait 2m"
}

if [ -z "${CCX_INSTALL_NO_MAIN:-}" ]; then
  main "$@"
fi
