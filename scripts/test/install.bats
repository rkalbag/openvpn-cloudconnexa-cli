#!/usr/bin/env bats
# Tests for scripts/install.sh. No network and no root: package tools are
# stubbed on PATH and log their arguments.

SCRIPT="$BATS_TEST_DIRNAME/../install.sh"
TOKEN="AbCdEfGh12345678.ijklmnop:QRSTUV+wx/yz=="

setup() {
  export CCX_INSTALL_NO_MAIN=1
  export OS_RELEASE="$BATS_TEST_TMPDIR/os-release"
  export APT_ETC="$BATS_TEST_TMPDIR/apt"
  STUBS="$BATS_TEST_TMPDIR/bin"
  LOG="$BATS_TEST_TMPDIR/log"
  mkdir -p "$STUBS"
  : >"$LOG"
  # Under `curl | bash` stdin is the rest of the script: tools must not read it.
  READS_STDIN='if read -r -t 1 x; then echo "stdin=$x" >>'"$LOG"'; fi'
}

# absent PATTERN: no log line matches.
absent() { [ "$(grep -c -- "$1" "$LOG")" -eq 0 ]; }

os_release() { printf '%s\n' "$@" >"$OS_RELEASE"; }

# stub NAME [BODY]: a command that logs "NAME args" and then runs BODY.
stub() {
  printf '#!/usr/bin/env bash\necho "%s $*" >>"%s"\n%s\n' "$1" "$LOG" "${2:-}" >"$STUBS/$1"
  chmod +x "$STUBS/$1"
}

# run_main runs the whole script as root with stubbed tools.
run_main() {
  stub id 'echo 0'
  stub apt-get "$READS_STDIN"
  stub curl 'while [ $# -gt 0 ]; do [ "$1" = -o ] && echo KEY >"$2"; shift; done'
  stub dnf "$READS_STDIN"
  stub subscription-manager
  stub uname 'echo x86_64'
  stub openvpn-connector-setup 'echo "baseurl=$CLOUDCONNEXA_BASEURL" >>'"$LOG"'
'"$READS_STDIN"
  PATH="$STUBS:$PATH" CCX_INSTALL_NO_MAIN= run bash "$SCRIPT" "$@"
}

load_script() {
  # shellcheck disable=SC1090
  source "$SCRIPT"
}

# --- argument parsing --------------------------------------------------------

@test "parse_args: token and --force in any order" {
  load_script
  parse_args --force "$TOKEN"
  [ "$TOKEN_ARG" = "$TOKEN" ]
  [ "$FORCE" = "--force" ]
  parse_args "$TOKEN"
  [ "$FORCE" = "" ]
}

@test "parse_args: - means stdin" {
  load_script
  parse_args -
  [ "$TOKEN_ARG" = "-" ]
}

@test "parse_args: missing token" {
  load_script
  run parse_args --force
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing token"* ]]
}

@test "parse_args: unknown option and two tokens" {
  load_script
  run parse_args "$TOKEN" --yes
  [ "$status" -eq 1 ]
  [[ "$output" == *"unknown option: --yes"* ]]
  run parse_args "$TOKEN" "$TOKEN"
  [ "$status" -eq 1 ]
  [[ "$output" == *"more than one token"* ]]
}

@test "parse_args: --help" {
  load_script
  run parse_args --help
  [ "$status" -eq 0 ]
  [[ "$output" == Usage:* ]]
}

# --- token -------------------------------------------------------------------

@test "read_token: from stdin, CRLF trimmed" {
  load_script
  want="$TOKEN"
  TOKEN=""
  TOKEN_ARG=-
  read_token < <(printf '%s\r\n' "$want")
  [ "$TOKEN" = "$want" ]
}

@test "read_token: empty stdin" {
  load_script
  TOKEN_ARG=-
  run read_token </dev/null
  [ "$status" -eq 1 ]
  [[ "$output" == *"no token on stdin"* ]]
}

@test "validate_token: rejects short or shell-unsafe tokens without echoing them" {
  load_script
  run validate_token "short"
  [ "$status" -eq 1 ]
  run validate_token 'AbCdEfGh12345678;rm -rf /'
  [ "$status" -eq 1 ]
  [[ "$output" != *"rm -rf"* ]]
  run validate_token "$TOKEN"
  [ "$status" -eq 0 ]
}

# --- distro detection --------------------------------------------------------

@test "detect_family: supported distributions" {
  load_script
  check() {
    os_release "${@:2}"
    [ "$(detect_family)" = "$1" ] || { echo "want $1 for $*, got $(detect_family)"; return 1; }
  }
  check ubuntu ID=ubuntu VERSION_ID='"24.04"' VERSION_CODENAME=noble
  check ubuntu ID=linuxmint ID_LIKE='"ubuntu debian"' VERSION_ID='"22"'
  check debian ID=debian VERSION_ID='"12"' VERSION_CODENAME=bookworm
  check debian ID=raspbian ID_LIKE=debian VERSION_ID='"12"'
  check rhel8 ID='"rhel"' ID_LIKE='"fedora"' VERSION_ID='"8.10"'
  check rhel9 ID='"rhel"' ID_LIKE='"fedora"' VERSION_ID='"9.4"'
  check el8 ID='"rocky"' ID_LIKE='"rhel centos fedora"' VERSION_ID='"8.9"'
  check el9 ID='"almalinux"' ID_LIKE='"rhel centos fedora"' VERSION_ID='"9.4"'
  check el9 ID='"centos"' ID_LIKE='"rhel fedora"' VERSION_ID='"9"'
  check el9 ID='"ol"' ID_LIKE='"fedora rhel"' VERSION_ID='"9.3"'
  check fedora ID=fedora VERSION_ID=40
}

@test "detect_family: unsupported distributions and versions" {
  load_script
  os_release ID=arch
  run detect_family
  [ "$status" -eq 1 ]
  [[ "$output" == *"unsupported distribution: arch"* ]]
  os_release ID='"centos"' VERSION_ID='"7"'
  run detect_family
  [ "$status" -eq 1 ]
  [[ "$output" == *"unsupported version"* ]]
  rm "$OS_RELEASE"
  run detect_family
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot detect"* ]]
}

@test "apt_codename: prefers UBUNTU_CODENAME" {
  load_script
  os_release ID=linuxmint VERSION_CODENAME=wilma UBUNTU_CODENAME=noble
  [ "$(apt_codename)" = noble ]
  os_release ID=debian VERSION_CODENAME=bookworm
  [ "$(apt_codename)" = bookworm ]
}

# --- whole script with stubbed tools -----------------------------------------

@test "main: Ubuntu install and enrolment" {
  os_release ID=ubuntu VERSION_ID='"24.04"' VERSION_CODENAME=noble
  run_main "$TOKEN" <<<"rest of the piped script"
  [ "$status" -eq 0 ]
  grep -q "^apt-get -y -q install python3-openvpn-connector-setup$" "$LOG"
  absent "policykit-1"
  grep -qx "deb \[signed-by=/etc/apt/keyrings/openvpn.asc\] https://packages.openvpn.net/openvpn3/debian noble main" \
    "$APT_ETC/sources.list.d/openvpn-packages.list"
  grep -qx "openvpn-connector-setup --token $TOKEN" "$LOG"
  grep -qx "baseurl=https://network-gateway.openvpn.com/network-gate/api/v1/profiles/" "$LOG"
  absent "^stdin="
  # The token is never printed.
  [[ "$output" != *"$TOKEN"* ]]
}

@test "main: Debian installs policykit and passes --force" {
  os_release ID=debian VERSION_ID='"12"' VERSION_CODENAME=bookworm
  run_main --force "$TOKEN"
  [ "$status" -eq 0 ]
  grep -q "install policykit-1" "$LOG"
  grep -qx "openvpn-connector-setup --token $TOKEN --force" "$LOG"
}

@test "main: token from stdin" {
  os_release ID=ubuntu VERSION_CODENAME=jammy
  run_main - <<<"$TOKEN"
  [ "$status" -eq 0 ]
  grep -qx "openvpn-connector-setup --token $TOKEN" "$LOG"
}

@test "main: RHEL 8 enables EPEL and CodeReady Builder" {
  os_release ID='"rhel"' VERSION_ID='"8.10"'
  run_main "$TOKEN"
  [ "$status" -eq 0 ]
  grep -qx "dnf -y install https://dl.fedoraproject.org/pub/epel/epel-release-latest-8.noarch.rpm" "$LOG"
  grep -qx "subscription-manager repos --enable codeready-builder-for-rhel-8-x86_64-rpms" "$LOG"
  grep -qx "dnf -y install https://packages.openvpn.net/openvpn-openvpn3-epel-repo-1-1.noarch.rpm" "$LOG"
  grep -qx "dnf -y install python3-openvpn-connector-setup" "$LOG"
}

@test "main: Rocky 8 enables PowerTools; Fedora uses COPR" {
  os_release ID='"rocky"' VERSION_ID='"8.9"'
  run_main "$TOKEN"
  [ "$status" -eq 0 ]
  grep -qx "dnf -y config-manager --set-enabled powertools" "$LOG"

  : >"$LOG"
  os_release ID=fedora VERSION_ID=40
  run_main "$TOKEN"
  [ "$status" -eq 0 ]
  grep -qx "dnf -y copr enable dsommers/openvpn3" "$LOG"
  absent "openvpn3-epel-repo"
}

@test "main: requires root" {
  os_release ID=ubuntu VERSION_CODENAME=noble
  run_main "$TOKEN"
  stub id 'echo 1000'
  PATH="$STUBS:$PATH" CCX_INSTALL_NO_MAIN= run bash "$SCRIPT" "$TOKEN"
  [ "$status" -eq 1 ]
  [[ "$output" == *"must run as root"* ]]
}

@test "main: setup failure gives a hint" {
  os_release ID=ubuntu VERSION_CODENAME=noble
  run_main "$TOKEN"
  stub openvpn-connector-setup 'exit 3'
  PATH="$STUBS:$PATH" CCX_INSTALL_NO_MAIN= run bash "$SCRIPT" "$TOKEN"
  [ "$status" -eq 1 ]
  [[ "$output" == *"ccx token <name>"* ]]
}
