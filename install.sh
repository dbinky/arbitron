#!/usr/bin/env bash
#
# Arbitron installer (P1: local install for macOS + Linux).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
#
# This script installs Arbitron as a user-level service under ~/.arbitron:
#   - macOS  -> launchd LaunchAgent (~/Library/LaunchAgents/dev.arbitron.plist)
#   - Linux  -> systemd --user unit (~/.config/systemd/user/arbitron.service)
#
# It is loopback-only (no TLS) for the local profile, requires NO sudo, and
# survives reboot (RunAtLoad / linger).
#
# Non-interactive overrides (for automation / tests): every prompt consults a
# matching ARBITRON_<VAR> environment variable first. See prompt_default /
# prompt_yes_no. The library can be sourced WITHOUT running main by setting
# ARBITRON_INSTALL_LIB_ONLY=1 (used by the bats suite).

set -euo pipefail
IFS=$'\n\t'

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# The GitHub repository that hosts releases and the raw install scripts. Every
# download/raw URL is built from this constant. NOTE: the Go module path
# (github.com/arbitron/arbitron) is an IMPORT path, not a URL — do not build
# download URLs from it.
# shellcheck disable=SC2034  # consumed by download_binary (Task 3) / P5 URL builders.
ARBITRON_REPO="${ARBITRON_REPO:-dbinky/arbitron}"

# Minimum dependency versions for the agent (claude-cli) toolchain.
MIN_NODE_MAJOR="${MIN_NODE_MAJOR:-18}"

# The npm package that provides the `claude` CLI.
CLAUDE_NPM_PKG="@anthropic-ai/claude-code"

# launchd label / systemd unit name.
ARBITRON_LAUNCHD_LABEL="dev.arbitron"
ARBITRON_SYSTEMD_UNIT="arbitron"

# ---------------------------------------------------------------------------
# Output / logging  (everything to stderr so stdout stays parseable)
# ---------------------------------------------------------------------------

# Colourise only when stderr is a TTY.
if [ -t 2 ]; then
  _C_RESET=$'\033[0m'; _C_INFO=$'\033[0;34m'; _C_WARN=$'\033[0;33m'; _C_ERR=$'\033[0;31m'
else
  _C_RESET=""; _C_INFO=""; _C_WARN=""; _C_ERR=""
fi

log_info()  { printf '%s==>%s %s\n' "$_C_INFO" "$_C_RESET" "$*" >&2; }
log_warn()  { printf '%sWARN:%s %s\n' "$_C_WARN" "$_C_RESET" "$*" >&2; }
log_error() { printf '%sERROR:%s %s\n' "$_C_ERR" "$_C_RESET" "$*" >&2; }

# die <msg...> — log an error and exit non-zero.
die() {
  log_error "$@"
  exit 1
}

# ---------------------------------------------------------------------------
# Prompt primitives (env override -> stdin -> default)
# ---------------------------------------------------------------------------

# prompt_default VAR "question" "default"
#   Echoes the resolved value to stdout. Resolution order:
#     1. ARBITRON_<VAR> env override (non-interactive),
#     2. a line typed on stdin,
#     3. the supplied default.
prompt_default() {
  local var="$1" question="$2" default="${3:-}"
  local envname="ARBITRON_${var}"
  if [ -n "${!envname:-}" ]; then
    printf '%s\n' "${!envname}"
    return 0
  fi
  local reply=""
  if [ -t 0 ]; then
    printf '%s [%s]: ' "$question" "$default" >&2
  fi
  IFS= read -r reply || true
  if [ -z "$reply" ]; then
    printf '%s\n' "$default"
  else
    printf '%s\n' "$reply"
  fi
}

# prompt_yes_no VAR "question" <yes|no default>
#   Returns 0 for yes, 1 for no. Honours ARBITRON_<VAR> ("yes"/"y"/"true"/"1"
#   -> yes; "no"/"n"/"false"/"0" -> no), then stdin, then the default.
prompt_yes_no() {
  local var="$1" question="$2" default="${3:-no}"
  local envname="ARBITRON_${var}"
  local reply=""
  if [ -n "${!envname:-}" ]; then
    reply="${!envname}"
  else
    if [ -t 0 ]; then
      printf '%s [y/n] (default %s): ' "$question" "$default" >&2
    fi
    IFS= read -r reply || true
  fi
  if [ -z "$reply" ]; then
    reply="$default"
  fi
  case "$(printf '%s' "$reply" | tr '[:upper:]' '[:lower:]')" in
    y|yes|true|1)  return 0 ;;
    n|no|false|0)  return 1 ;;
    *)             [ "$default" = "yes" ] && return 0 || return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# Platform / state detection
# ---------------------------------------------------------------------------

# _uname_s / _uname_m honour ARBITRON_UNAME_* overrides so the detectors are
# unit-testable without faking the uname binary.
_uname_s() { printf '%s\n' "${ARBITRON_UNAME_S:-$(uname -s)}"; }
_uname_m() { printf '%s\n' "${ARBITRON_UNAME_M:-$(uname -m)}"; }
_uid()     { printf '%s\n' "${ARBITRON_UID:-$(id -u)}"; }

# detect_os -> macos | linux (errors otherwise).
detect_os() {
  case "$(_uname_s)" in
    Darwin) printf 'macos\n' ;;
    Linux)  printf 'linux\n' ;;
    *)      log_error "unsupported OS: $(_uname_s)"; return 1 ;;
  esac
}

# detect_arch -> amd64 | arm64 (errors otherwise).
detect_arch() {
  case "$(_uname_m)" in
    x86_64|amd64)   printf 'amd64\n' ;;
    aarch64|arm64)  printf 'arm64\n' ;;
    *)              log_error "unsupported architecture: $(_uname_m)"; return 1 ;;
  esac
}

# is_elevated -> 0 if running as root. Local install must NOT require elevation,
# so a non-zero return (non-root) is the normal local path.
is_elevated() {
  [ "$(_uid)" -eq 0 ]
}

# select_install_type -> Local | Shared.
# macOS is local-only (no Shared path). On Linux the operator chooses Local
# (per-user, ~/.arbitron, no sudo) or Shared (system-wide under /etc + /var with
# a dedicated service account). ARBITRON_INSTALL_TYPE forces the answer
# non-interactively; otherwise we prompt, defaulting to Shared when running as
# root (the elevated-install signal) and Local otherwise.
select_install_type() {
  local os
  os="$(detect_os)" || return 1
  case "$os" in
    macos) printf 'Local\n' ;;
    linux)
      local forced="${ARBITRON_INSTALL_TYPE:-}"
      if [ -n "$forced" ]; then
        case "$(printf '%s' "$forced" | tr '[:upper:]' '[:lower:]')" in
          local)  printf 'Local\n';  return 0 ;;
          shared) printf 'Shared\n'; return 0 ;;
          *)      log_error "unknown ARBITRON_INSTALL_TYPE '$forced' (want local|shared)"; return 1 ;;
        esac
      fi
      local def="local"
      is_elevated && def="shared"
      local choice
      choice="$(prompt_default INSTALL_TYPE 'Install type — local (per-user) or shared (system-wide)' "$def")"
      case "$(printf '%s' "$choice" | tr '[:upper:]' '[:lower:]')" in
        local)  printf 'Local\n' ;;
        shared) printf 'Shared\n' ;;
        *)      log_error "unknown install type '$choice'"; return 1 ;;
      esac
      ;;
    *)     return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# Shared (Linux system) install — path globals.
#
# The Shared branch installs system-wide under /etc + /var with a dedicated
# service account; the Local branch (P1) lives under ~/.arbitron. ARB_INSTALL_MODE
# (local|shared) drives which layout the layout_* resolvers below return, so call
# sites do not fork. Each global is overridable (the bats suite redirects them
# into a sandbox; the operator never sets them directly).
# ---------------------------------------------------------------------------

ARB_INSTALL_MODE="${ARB_INSTALL_MODE:-local}"
ARB_PREFIX_BIN="${ARB_PREFIX_BIN:-/usr/local/bin}"
ARB_ETC="${ARB_ETC:-/etc/arbitron}"
ARB_LIB="${ARB_LIB:-/var/lib/arbitron}"
ARB_LOG="${ARB_LOG:-/var/log/arbitron}"
# Service account for the system install; Task 2's Q&A may override it.
ARB_SVC_USER="${ARB_SVC_USER:-arbitron}"
# systemd SYSTEM unit path (overridable so bats can write into the sandbox).
ARB_SYSTEMD_UNIT_PATH="${ARB_SYSTEMD_UNIT_PATH:-/etc/systemd/system/arbitron.service}"

# _is_shared -> 0 when the install is the system (shared) profile.
_is_shared() { [ "${ARB_INSTALL_MODE:-local}" = "shared" ]; }

# ---------------------------------------------------------------------------
# Path layout. The resolvers are mode-aware: in shared mode they return the
# system paths (/etc, /var/lib, /var/log, /usr/local/bin), otherwise the P1
# local paths under ~/.arbitron.
# ---------------------------------------------------------------------------

arbitron_home()      { printf '%s/.arbitron\n' "$HOME"; }
layout_bin() {
  if _is_shared; then printf '%s\n' "$ARB_PREFIX_BIN"; else printf '%s/bin\n' "$(arbitron_home)"; fi
}
layout_config() {
  if _is_shared; then printf '%s/config.yaml\n' "$ARB_ETC"; else printf '%s/config.yaml\n' "$(arbitron_home)"; fi
}
layout_data() {
  if _is_shared; then printf '%s\n' "$ARB_LIB"; else printf '%s/data\n' "$(arbitron_home)"; fi
}
layout_db() {
  if _is_shared; then printf '%s/arbitron.db\n' "$ARB_LIB"; else printf '%s/data/arbitron.db\n' "$(arbitron_home)"; fi
}
layout_workspaces() {
  if _is_shared; then printf '%s/workspaces\n' "$ARB_LIB"; else printf '%s/workspaces\n' "$(arbitron_home)"; fi
}
layout_logs() {
  if _is_shared; then printf '%s\n' "$ARB_LOG"; else printf '%s/logs\n' "$(arbitron_home)"; fi
}
layout_secrets_env() {
  if _is_shared; then printf '%s/arbitron.env\n' "$ARB_ETC"; else printf '%s/arbitron.env\n' "$(arbitron_home)"; fi
}
layout_runtime() {
  if _is_shared; then printf '%s/runtime\n' "$ARB_LIB"; else printf '%s/runtime\n' "$(arbitron_home)"; fi
}

# ---------------------------------------------------------------------------
# Checksum helpers
# ---------------------------------------------------------------------------

# sha256_tool reads stdin and prints the 64-hex digest, picking sha256sum
# (Linux) or `shasum -a 256` (macOS).
sha256_tool() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  else
    log_error "no sha256 tool found (need sha256sum or shasum)"
    return 1
  fi
}

# verify_checksum <file> <expected_sha256> -> 0 on match, non-zero on mismatch.
# Comparison is case-insensitive (SHA256SUMS may use either hex case).
verify_checksum() {
  local file="$1" expected="$2" actual
  [ -f "$file" ] || { log_error "verify_checksum: missing file $file"; return 1; }
  actual="$(sha256_tool <"$file")" || return 1
  if [ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" != "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ]; then
    log_error "checksum mismatch for $file: expected $expected got $actual"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# The canonical FAIL-CLOSED verification contract (P5 Task 2).
#
# verify_download is the single gate every release artifact passes before it is
# made executable or installed. It is MANDATORY and has NO bypass: any of
#   { missing SHA256SUMS, artifact not listed, hash mismatch, no hash tool }
# aborts (non-zero), deletes the partial download, and leaves nothing in place.
# verify_signature is OPTIONAL defense-in-depth (cosign); its absence NEVER
# bypasses the checksum gate.
# ---------------------------------------------------------------------------

# verify_download <downloaded_file> <artifact_basename> <sha256sums_file>
#   Read <artifact_basename>'s expected hash out of <sha256sums_file>, compute the
#   actual hash of <downloaded_file>, and compare. On ANY failure: log, `rm -f`
#   the download, return non-zero. On match: return 0 (the file is left in place
#   for the caller to install). FAIL-CLOSED; no skip/insecure path.
verify_download() {
  local file="$1" name="$2" sums="$3" expected
  if [ ! -f "$sums" ]; then
    log_error "could not fetch/read SHA256SUMS ($sums); refusing to install"
    rm -f "$file"
    return 1
  fi
  # SHA256SUMS lines are "<hash>  <name>" (sha256sum) or "<hash> *<name>" (binary
  # mode). Match the exact basename only.
  expected="$(awk -v a="$name" '$2==a || $2=="*"a {print $1; exit}' "$sums")"
  if [ -z "$expected" ]; then
    log_error "no checksum for $name in SHA256SUMS; refusing to install"
    rm -f "$file"
    return 1
  fi
  if ! verify_checksum "$file" "$expected"; then
    log_error "refusing to install unverified artifact: $name"
    rm -f "$file"
    return 1
  fi
  return 0
}

# verify_signature <sha256sums_file> [sig_file] [pem_file]
#   OPTIONAL cosign verification of SHA256SUMS (keyless, Task-1 identity). Runs
#   only when cosign is present OR ARBITRON_VERIFY_SIGNATURE=1. When requested and
#   cosign fails -> non-zero (fail-closed for the signature too). When cosign is
#   absent and not explicitly requested -> a one-line notice + return 0 (the
#   checksum gate already passed and is never bypassed here).
verify_signature() {
  local sums="$1"
  local sig="${2:-${sums}.sig}"
  local pem="${3:-${sums}.pem}"
  local want="${ARBITRON_VERIFY_SIGNATURE:-}"
  if ! require_cmd cosign; then
    if [ "$want" = "1" ]; then
      log_error "ARBITRON_VERIFY_SIGNATURE=1 but cosign is not installed"
      return 1
    fi
    log_info "cosign not found; skipping optional signature check (checksum already verified)"
    return 0
  fi
  if [ ! -f "$sig" ] || [ ! -f "$pem" ]; then
    if [ "$want" = "1" ]; then
      log_error "signature verification requested but $sig / $pem is missing"
      return 1
    fi
    log_info "no signature/cert alongside SHA256SUMS; skipping optional signature check"
    return 0
  fi
  log_info "verifying SHA256SUMS signature with cosign"
  if ! cosign verify-blob \
      --certificate "$pem" \
      --signature "$sig" \
      --certificate-identity-regexp "https://github.com/${ARBITRON_REPO}/.*" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      "$sums" >/dev/null 2>&1; then
    log_error "cosign signature verification FAILED for SHA256SUMS"
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# Command presence / version
# ---------------------------------------------------------------------------

# require_cmd <name> -> 0 if on PATH, 1 otherwise (RETURNS, does not exit, so the
# caller can branch into dependency resolution).
require_cmd() {
  command -v "$1" >/dev/null 2>&1
}

# command_version <cmd> [args...] -> prints the first semver-ish token found in
# `<cmd> --version` output (or the given args). Empty + non-zero if none.
command_version() {
  local cmd="$1"; shift
  local out
  if [ "$#" -gt 0 ]; then
    out="$("$cmd" "$@" 2>/dev/null)" || true
  else
    out="$("$cmd" --version 2>/dev/null)" || true
  fi
  printf '%s' "$out" | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -n1
}

# ---------------------------------------------------------------------------
# Dependency resolution (agent / claude-cli toolchain)
#
# Arbitron itself has ZERO runtime deps (self-contained CGO_ENABLED=0 binary).
# Everything below is for the agent features (the claude-cli provider path); an
# API-key-only deployment can skip the whole toolchain.
#
# Per dependency: presence + min version -> OS package manager ->
# bootstrap-the-package-manager -> checksum-verified direct download.
# Local install runs package managers as the user (NO sudo). The marked seam is
# where P2 prefixes sudo for the system install.
# ---------------------------------------------------------------------------

# detect_pkg_mgr -> brew (macOS) or the first present of apt-get/dnf/pacman/
# zypper (Linux), in that documented precedence; empty if none.
detect_pkg_mgr() {
  local os
  os="$(detect_os)" || return 1
  if [ "$os" = "macos" ]; then
    require_cmd brew && printf 'brew\n'
    return 0
  fi
  local mgr
  for mgr in apt-get dnf pacman zypper; do
    if require_cmd "$mgr"; then
      printf '%s\n' "$mgr"
      return 0
    fi
  done
  return 0
}

# pkg_install <pkg...> — install packages via the detected manager with the
# correct non-interactive flags. Local: no sudo.
# P2 seam: prefix the dispatched command with `sudo` for the system install.
pkg_install() {
  local mgr
  mgr="$(detect_pkg_mgr)"
  [ -n "$mgr" ] || { log_error "no supported package manager found"; return 1; }
  local sudo_prefix="${ARBITRON_PKG_SUDO:-}"   # empty on local; P2 sets "sudo".
  case "$mgr" in
    brew)    $sudo_prefix brew install "$@" ;;
    apt-get) $sudo_prefix apt-get install -y "$@" ;;
    dnf)     $sudo_prefix dnf install -y "$@" ;;
    pacman)  $sudo_prefix pacman -S --noconfirm "$@" ;;
    zypper)  $sudo_prefix zypper install -y "$@" ;;
    *)       log_error "unsupported package manager: $mgr"; return 1 ;;
  esac
}

# bootstrap_pkg_mgr — on macOS, offer to install Homebrew. Returns success when
# the bootstrap ran, non-zero when declined/unsupported so the caller can fall
# back to a direct download.
bootstrap_pkg_mgr() {
  local os
  os="$(detect_os)" || return 1
  if [ "$os" != "macos" ]; then
    log_warn "no package-manager bootstrap available for $os"
    return 1
  fi
  if ! prompt_yes_no BOOTSTRAP_BREW "Homebrew not found. Install it now?" yes; then
    log_warn "Homebrew bootstrap declined"
    return 1
  fi
  log_info "bootstrapping Homebrew"
  # The official installer is itself fetched over curl; in tests curl is mocked.
  /bin/bash -c \
    "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)" \
    </dev/null
}

# download_verified <url> <sha256> <dest>
#   curl the URL to a temp file, verify_checksum it, then move into place. Aborts
#   (non-zero, leaving NO artifact at dest) on any download or checksum failure.
#   This is the shared verified-download primitive Task 3 reuses for the binary.
download_verified() {
  local url="$1" sha="$2" dest="$3"
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/arbitron-dl.XXXXXX")" || return 1
  if ! curl -fsSL -o "$tmp" "$url"; then
    log_error "download failed: $url"
    rm -f "$tmp"
    return 1
  fi
  if ! verify_checksum "$tmp" "$sha"; then
    log_error "refusing to install unverified artifact from $url"
    rm -f "$tmp"
    return 1
  fi
  mkdir -p "$(dirname "$dest")"
  mv "$tmp" "$dest"
}

# _node_major prints node's major version (from `node --version` -> vX.Y.Z).
_node_major() {
  command_version node | cut -d. -f1
}

# ensure_node — presence + min-major check, else pkg_install node, else bootstrap
# the package manager and retry. (Direct-download fallback for node is a
# documented P2/P5 extension; on the local path the package manager / bootstrap
# covers macOS and the supported Linux distros.)
ensure_node() {
  local major=""
  if require_cmd node; then
    major="$(_node_major)"
    if [ -n "$major" ] && [ "$major" -ge "$MIN_NODE_MAJOR" ] 2>/dev/null; then
      log_info "node $(command_version node) satisfies the minimum (>= $MIN_NODE_MAJOR)"
      return 0
    fi
    log_warn "node $(command_version node) is below the minimum (>= $MIN_NODE_MAJOR); upgrading"
  else
    log_info "node not found; installing"
  fi

  if [ -n "$(detect_pkg_mgr)" ]; then
    pkg_install node && return 0
  fi
  if bootstrap_pkg_mgr; then
    pkg_install node && return 0
  fi
  # Fallback seam: direct-download node into the Arbitron runtime prefix. The
  # exact artifact URL + checksum are pinned in P5; for P1 we surface a clear
  # error if neither a package manager nor bootstrap succeeded.
  log_error "could not install node via a package manager or bootstrap"
  return 1
}

# ensure_git / ensure_gh — package-manager-first; both are agent-path deps.
ensure_git() {
  if require_cmd git; then
    log_info "git present"
    return 0
  fi
  log_info "git not found; installing"
  pkg_install git || { bootstrap_pkg_mgr && pkg_install git; }
}

ensure_gh() {
  if require_cmd gh; then
    log_info "gh present"
    return 0
  fi
  log_info "gh not found; installing"
  pkg_install gh || { bootstrap_pkg_mgr && pkg_install gh; }
}

# ensure_claude — if a satisfying `claude` exists, record its path; else install
# @anthropic-ai/claude-code via npm into the Arbitron-owned npm prefix
# ($(layout_runtime)/npm) so it never fights a system Node. The resolved claude
# path is exported as ARBITRON_CLAUDE_PATH for Task 3 to write as llm.binarypath.
ensure_claude() {
  if require_cmd claude; then
    ARBITRON_CLAUDE_PATH="$(command -v claude)"
    export ARBITRON_CLAUDE_PATH
    log_info "claude present at $ARBITRON_CLAUDE_PATH"
    return 0
  fi

  ensure_node || return 1
  require_cmd npm || { log_error "npm not found after installing node"; return 1; }

  local prefix
  prefix="$(layout_runtime)/npm"
  mkdir -p "$prefix"
  log_info "installing $CLAUDE_NPM_PKG into the Arbitron npm prefix ($prefix)"
  npm_config_prefix="$prefix" npm install -g "$CLAUDE_NPM_PKG" --prefix "$prefix" || return 1

  ARBITRON_CLAUDE_PATH="$prefix/bin/claude"
  export ARBITRON_CLAUDE_PATH
}

# ensure_opencode — OPTIONAL. Prompted; a decline is non-fatal.
ensure_opencode() {
  if require_cmd opencode; then
    log_info "opencode present"
    return 0
  fi
  if ! prompt_yes_no INSTALL_OPENCODE "Install the optional 'opencode' agent backend?" no; then
    log_info "skipping optional opencode"
    return 0
  fi
  pkg_install opencode || log_warn "opencode install failed (optional); continuing"
  return 0
}

# resolve_dependencies — orchestrate the agent toolchain in order. Only runs when
# the install will use the claude-cli / subscription path; an API-key-only
# install (ARBITRON_LLM_ACCESS=api-key) skips node/claude entirely.
resolve_dependencies() {
  local access="${ARBITRON_LLM_ACCESS:-subscription}"
  if [ "$access" = "api-key" ]; then
    log_info "API-key install: skipping the agent (claude-cli) toolchain"
    return 0
  fi
  log_info "resolving agent toolchain dependencies"
  ensure_node   || return 1
  ensure_git    || return 1
  ensure_gh     || return 1
  ensure_claude || return 1
  ensure_opencode
}

# ---------------------------------------------------------------------------
# Config Q&A, config.yaml + secrets env, binary download
#
# run_config_qa asks the short "easy questions" and stores the answers in module
# globals (CFG_*). write_config_yaml / write_secrets_env consume them. The master
# key is the ARBITRON_MASTER_KEY env var (base64 of 32 random bytes), read by the
# binary from the environment — it is NEVER a YAML key, NEVER logged.
# ---------------------------------------------------------------------------

# Answer globals (populated by run_config_qa).
CFG_PORT=""
CFG_LLM_ACCESS=""
CFG_SKIP_PERMS=""
CFG_DATA_DIR=""
CFG_API_KEY=""
# MASTER_KEY holds the (sensitive) base64 master key in memory; never echoed.
MASTER_KEY=""

# read_existing_config_value <dotted.key> — grep a scalar value out of an existing
# config.yaml for reconfigure pre-fill. Handles the simple `key: value` and a
# one-level-nested `parent:\n  child: value` shape used by our writer. Prints the
# unquoted value, empty if absent.
read_existing_config_value() {
  local key="$1" cfg
  cfg="$(layout_config)"
  [ -f "$cfg" ] || return 0
  local parent child
  if [[ "$key" == *.* ]]; then
    parent="${key%%.*}"
    child="${key#*.}"
    awk -v p="$parent:" -v c="$child:" '
      $0 ~ "^"p { inblk=1; next }
      inblk && /^[^[:space:]]/ { inblk=0 }
      inblk && $1==c { $1=""; sub(/^[[:space:]]+/,""); gsub(/"/,""); print; exit }
    ' "$cfg"
  else
    awk -v c="$key:" '$1==c { $1=""; sub(/^[[:space:]]+/,""); gsub(/"/,""); print; exit }' "$cfg"
  fi
}

# run_config_qa — ask port, LLM access, skip-permissions (with the RCE warning),
# and data location. Pre-fills defaults from an existing config on reconfigure.
run_config_qa() {
  local def_port="8080"
  local existing_port
  existing_port="$(read_existing_config_value server.port)"
  [ -n "$existing_port" ] && def_port="$existing_port"
  CFG_PORT="$(prompt_default PORT "Dashboard port" "$def_port")"

  CFG_LLM_ACCESS="$(prompt_default LLM_ACCESS \
    "LLM access — 'subscription' (Claude login) or 'api-key' (Anthropic key)" \
    "subscription")"

  if [ "$CFG_LLM_ACCESS" = "api-key" ]; then
    CFG_API_KEY="$(prompt_default API_KEY "Paste your Anthropic API key" "")"
    if [ -z "${CFG_API_KEY:-}" ]; then
      die "api-key mode requires a non-empty Anthropic API key. Re-run with ARBITRON_API_KEY set or choose 'subscription'."
    fi
  fi

  # The --dangerously-skip-permissions question, asked EXPLICITLY with its
  # RCE-equivalent warning. Never silently defaulted.
  log_warn "Skip tool-permission prompts enables --dangerously-skip-permissions:"
  log_warn "  the agent runs ANY tool (Bash/Write/Edit) with no human approval."
  log_warn "  This is RCE-equivalent whenever a prompt input is attacker-influenced."
  if prompt_yes_no SKIP_PERMS "Enable unattended agent tools (skip permissions)?" no; then
    CFG_SKIP_PERMS="true"
  else
    CFG_SKIP_PERMS="false"
  fi

  CFG_DATA_DIR="$(prompt_default DATA_DIR "Data location" "$(layout_data)")"
}

# generate_master_key — reuse an existing master key on reconfigure; otherwise
# generate 32 random bytes -> base64 into MASTER_KEY. Never echoed.
generate_master_key() {
  local env
  env="$(layout_secrets_env)"
  if [ -f "$env" ] && grep -q '^ARBITRON_MASTER_KEY=' "$env"; then
    MASTER_KEY="$(grep '^ARBITRON_MASTER_KEY=' "$env" | head -n1 | cut -d= -f2-)"
    log_info "reusing the existing master key"
    return 0
  fi
  if command -v openssl >/dev/null 2>&1; then
    MASTER_KEY="$(openssl rand -base64 32)"
  else
    MASTER_KEY="$(head -c 32 /dev/urandom | base64)"
  fi
  log_info "generated a new master key"
}

# write_secrets_env — write ~/.arbitron/arbitron.env (0600) with the master key
# and, for api-key mode, the Anthropic key. Created under umask 077.
write_secrets_env() {
  local env
  env="$(layout_secrets_env)"
  mkdir -p "$(dirname "$env")"
  local old_umask
  old_umask="$(umask)"
  umask 077
  {
    printf '# Arbitron secrets — sourced by the service. DO NOT commit.\n'
    printf 'ARBITRON_MASTER_KEY=%s\n' "$MASTER_KEY"
    if [ "${CFG_LLM_ACCESS:-}" = "api-key" ] && [ -n "${CFG_API_KEY:-}" ]; then
      printf 'ANTHROPIC_API_KEY=%s\n' "$CFG_API_KEY"
    fi
  } >"$env"
  umask "$old_umask"
  chmod 600 "$env"
}

# write_config_yaml — emit config.yaml with ABSOLUTE paths and a LOOPBACK bind.
write_config_yaml() {
  local cfg data
  cfg="$(layout_config)"
  data="${CFG_DATA_DIR:-$(layout_data)}"

  # Create the on-disk tree.
  mkdir -p "$(layout_bin)" "$data" "$(layout_workspaces)" "$(layout_logs)"
  chmod 700 "$(arbitron_home)" 2>/dev/null || true

  local provider llm_block
  if [ "${CFG_LLM_ACCESS:-subscription}" = "api-key" ]; then
    provider="anthropic"
    llm_block="  provider: \"anthropic\""
  else
    provider="claude-cli"
    llm_block="  provider: \"claude-cli\"
  binarypath: \"${ARBITRON_CLAUDE_PATH:-}\""
  fi
  : "$provider"  # documented selection; value embedded in llm_block.

  local port="${CFG_PORT:-8080}"
  local skip="${CFG_SKIP_PERMS:-false}"

  cat >"$cfg" <<EOF
# Arbitron config (generated by install.sh). Local profile: loopback-only.
server:
  host: "127.0.0.1"
  port: ${port}
database:
  path: "${data}/arbitron.db"
workspace:
  path: "$(layout_workspaces)"
llm:
${llm_block}
  skippermissions: ${skip}
logging:
  level: "info"
  file: "$(layout_logs)/arbitron.log"
auth:
  mode: "local"
EOF
  chmod 600 "$cfg"
}

# claude_login — subscription mode only. Export CLAUDE_CONFIG_DIR under the data
# dir and run `claude /login` interactively. Skipped in non-interactive runs.
claude_login() {
  [ "${CFG_LLM_ACCESS:-subscription}" = "api-key" ] && return 0
  local ccd="${CFG_DATA_DIR:-$(layout_data)}/claude"
  mkdir -p "$ccd"
  export CLAUDE_CONFIG_DIR="$ccd"
  if [ "${ARBITRON_SKIP_CLAUDE_LOGIN:-}" = "1" ] || [ ! -t 0 ]; then
    log_info "skipping interactive 'claude /login' (non-interactive run)"
    return 0
  fi
  local claude_bin="${ARBITRON_CLAUDE_PATH:-claude}"
  log_info "launching 'claude /login' (authenticate your Claude subscription)"
  "$claude_bin" /login || log_warn "claude /login did not complete; re-run it later"
}

# _arbitron_latest_tag — follow the releases/latest redirect (NO API token, no
# rate-limit) and echo the final tag's version. The effective URL ends in
# `/tag/v<ver>`; parse <ver>. Falls back to the GitHub API `.tag_name` when the
# redirect parse fails. Isolated behind `curl` so the bats suite can stub it.
_arbitron_latest_tag() {
  local effective ver
  effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${ARBITRON_REPO}/releases/latest" 2>/dev/null || true)"
  ver="$(printf '%s' "$effective" | sed -n 's#.*/tag/v\{0,1\}\([0-9][^/[:space:]]*\).*#\1#p')"
  if [ -z "$ver" ]; then
    # API fallback: parse `"tag_name": "v1.2.3"`.
    ver="$(curl -fsSL "https://api.github.com/repos/${ARBITRON_REPO}/releases/latest" 2>/dev/null \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)"
  fi
  printf '%s' "$ver"
}

# arbitron_resolve_version — resolve the release version to install.
#   - ARBITRON_VERSION set  -> use it verbatim, normalised to NO leading `v`
#     (accepts both `1.2.3` and `v1.2.3`).
#   - unset                 -> the latest release, via the releases/latest
#     redirect (API fallback).
# Fails closed (non-zero) when no version can be determined.
arbitron_resolve_version() {
  local version="${ARBITRON_VERSION:-}"
  if [ -n "$version" ]; then
    printf '%s\n' "${version#v}"
    return 0
  fi
  version="$(_arbitron_latest_tag)"
  version="${version#v}"
  if [ -z "$version" ]; then
    log_error "could not resolve the latest Arbitron release version; pin one with ARBITRON_VERSION"
    return 1
  fi
  printf '%s\n' "$version"
}

# arbitron_artifact_name <version> <os> <arch> — the release artifact basename
# `arbitron_<version>_<os>_<arch>` (+ `.exe` on windows). Pure string in/out.
arbitron_artifact_name() {
  local version="$1" os="$2" arch="$3"
  local name="arbitron_${version}_${os}_${arch}"
  [ "$os" = "windows" ] && name="${name}.exe"
  printf '%s\n' "$name"
}

# arbitron_release_base <version> — the release download base URL for a resolved
# (concrete) version, built from the canonical ARBITRON_REPO constant (NOT the Go
# module path). The version is always concrete here (arbitron_resolve_version
# turns `latest` into a real tag first), so the URL points at the pinned tag.
arbitron_release_base() {
  local version="${1#v}"
  printf 'https://github.com/%s/releases/download/v%s\n' "$ARBITRON_REPO" "$version"
}

# fetch_binary_to <dest> — resolve version + os/arch, fetch the artifact and its
# SHA256SUMS, verify, and place the verified binary at <dest>. Aborts (non-zero,
# no artifact at <dest>) on any download or checksum failure. Shared with the
# system install (install_binary_system) so the verify contract is identical.
fetch_binary_to() {
  local dest="$1"
  local version os arch base artifact tmpdir sumsfile tmpbin
  version="$(arbitron_resolve_version)" || return 1
  os="$(detect_os)"   || return 1
  arch="$(detect_arch)" || return 1
  base="$(arbitron_release_base "$version")"

  # The release artifact uses the Go-style OS token (darwin/linux).
  local ostoken="$os"
  [ "$os" = "macos" ] && ostoken="darwin"
  artifact="$(arbitron_artifact_name "$version" "$ostoken" "$arch")"

  tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/arbitron-bin.XXXXXX")" || return 1

  # Fetch SHA256SUMS (+ optional signature material) into the temp dir.
  sumsfile="$tmpdir/SHA256SUMS"
  if ! curl -fsSL -o "$sumsfile" "${base}/SHA256SUMS"; then
    log_error "could not fetch SHA256SUMS from ${base}"
    rm -rf "$tmpdir"
    return 1
  fi
  # Best-effort fetch of the cosign material; verify_signature decides if it is
  # required. A missing .sig/.pem never bypasses the (already mandatory) checksum.
  curl -fsSL -o "${sumsfile}.sig" "${base}/SHA256SUMS.sig" 2>/dev/null || true
  curl -fsSL -o "${sumsfile}.pem" "${base}/SHA256SUMS.pem" 2>/dev/null || true

  # Download the binary to a temp file, then run the mandatory fail-closed gate
  # BEFORE anything is made executable or moved into place.
  log_info "downloading $artifact"
  tmpbin="$tmpdir/$artifact"
  if ! curl -fsSL -o "$tmpbin" "${base}/${artifact}"; then
    log_error "download failed: ${base}/${artifact}"
    rm -rf "$tmpdir"
    return 1
  fi
  if ! verify_download "$tmpbin" "$artifact" "$sumsfile"; then
    log_error "binary download/verification failed"
    rm -rf "$tmpdir"
    return 1
  fi
  if ! verify_signature "$sumsfile"; then
    log_error "signature verification failed; refusing to install"
    rm -rf "$tmpdir"
    return 1
  fi

  mkdir -p "$(dirname "$dest")"
  mv "$tmpbin" "$dest"
  rm -rf "$tmpdir"
}

# download_binary — local install: fetch the verified binary into
# $(layout_bin)/arbitron mode 0755. Aborts on a mismatch leaving no binary.
download_binary() {
  local bin
  bin="$(layout_bin)/arbitron"
  mkdir -p "$(layout_bin)"
  fetch_binary_to "$bin" || return 1
  chmod 0755 "$bin"
  log_info "installed arbitron at $bin"
}

# ===========================================================================
# SHARED (Linux system) install — P2
#
# Selected when select_install_type returns "Shared" (Linux only). System-wide
# layout under /etc + /var, a dedicated service account, a systemd SYSTEM unit,
# and multi-user auth (edge+local or OIDC) with optional public TLS exposure.
# Reuses the P1 helpers above (detect_os/detect_arch, resolve_dependencies,
# download_verified via fetch path, prompt_*/log_*, the layout_* resolvers in
# shared mode, read_existing_config_value) rather than duplicating them.
# ===========================================================================

# --- Task 1: elevation, service account, system layout, binary --------------

# require_root — the Shared path is system-level and MUST run elevated. We do
# NOT silently self-sudo (the privilege boundary stays explicit): if not root we
# die, pointing at sudo when it is available.
require_root() {
  if [ "$(_uid)" -eq 0 ]; then
    return 0
  fi
  if require_cmd sudo; then
    die "shared (system) install requires root; re-run with sudo: sudo bash install.sh"
  fi
  die "shared (system) install requires root and sudo was not found; re-run as root"
}

# ensure_service_account — create the dedicated, no-login system account that the
# service runs as, when absent; reuse it otherwise. Honours a custom ARB_SVC_USER.
ensure_service_account() {
  local user="${ARB_SVC_USER:-arbitron}"
  if getent passwd "$user" >/dev/null 2>&1; then
    log_info "service account '$user' already exists; reusing it"
    return 0
  fi
  log_info "creating dedicated system account '$user'"
  local shell="/usr/sbin/nologin"
  [ -x /usr/sbin/nologin ] || [ -e /usr/sbin/nologin ] || shell="/bin/false"
  useradd --system --no-create-home --home-dir "$ARB_LIB" \
    --shell "$shell" "$user"
}

# _make_dir <path> <mode> <owner> — create a directory with the given mode and
# owner. Prefers `install -d` (atomic mode+owner); falls back to mkdir/chmod/chown.
_make_dir() {
  local path="$1" mode="$2" owner="$3"
  if require_cmd install; then
    install -d -m "$mode" -o "${owner%%:*}" -g "${owner##*:}" "$path"
  else
    mkdir -p "$path"
    chmod "$mode" "$path"
    chown "$owner" "$path"
  fi
}

# create_system_layout — lay down /etc/arbitron (0755 root:root),
# /var/lib/arbitron + workspaces (0750 svc-account) and /var/log/arbitron (0750
# svc-account). Data dirs are NOT world-readable.
create_system_layout() {
  local user="${ARB_SVC_USER:-arbitron}"
  _make_dir "$ARB_ETC" 0755 "root:root"
  _make_dir "$ARB_LIB" 0750 "${user}:${user}"
  _make_dir "$ARB_LIB/workspaces" 0750 "${user}:${user}"
  _make_dir "$ARB_LOG" 0750 "${user}:${user}"
}

# install_binary_system — fetch the verified binary and place it at
# /usr/local/bin/arbitron, 0755 root:root.
install_binary_system() {
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/arbitron-sysbin.XXXXXX")" || return 1
  if ! fetch_binary_to "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  install -m 0755 -o root -g root "$tmp" "$ARB_PREFIX_BIN/arbitron"
  rm -f "$tmp"
  log_info "installed arbitron at $ARB_PREFIX_BIN/arbitron"
}

# --- Task 2: shared config Q&A + config.yaml + arbitron.env -----------------
#
# CANONICAL shared Q&A order (P2 ↔ P4 parity — install.ps1 mirrors this verbatim):
#   1. Bind host / port            (default host 0.0.0.0, port 8080)
#   2. Auth model                  (edge+local | OIDC org)
#   3. If OIDC: issuer -> client id -> client secret(env) -> redirect URL
#      (= publicbaseurl + /auth/callback) -> bootstrap admins -> optional RBAC
#      (group claim -> default group -> department leads)
#   4. TLS model                   (tunnel/proxy | direct cert+key | autocert)
#   5. Unattended agent tools?     (--dangerously-skip-permissions; RCE warning)
#   6. Service account name        (default arbitron)
#   7. Data location               (default /var/lib/arbitron)
#   8. Master key                  (auto-generated silently into arbitron.env)
#
# P0 TLS invariants encoded as prompt guards: cert XOR autocert; autocert =>
# port 443 (+ autocertcachedir); local mode + TLS on a non-loopback host =>
# server.tls.allowunauthenticated=true only after an explicit extra confirm.

# Shared answer globals (populated by run_shared_qa).
SCFG_HOST=""
SCFG_PORT=""
SCFG_AUTH_MODE=""            # local | org
SCFG_PUBLIC_BASE_URL=""
SCFG_COOKIE_SECURE="true"
SCFG_OIDC_ISSUER=""
SCFG_OIDC_CLIENT_ID=""
SCFG_OIDC_CLIENT_SECRET=""   # -> arbitron.env, never config.yaml
SCFG_OIDC_REDIRECT=""
SCFG_BOOTSTRAP_ADMINS=""
SCFG_RBAC="false"
SCFG_RBAC_GROUP_CLAIM=""
SCFG_RBAC_DEFAULT_GROUP="default"
SCFG_RBAC_LEADS=""           # "dept=a,b;dept2=c"
SCFG_TLS_ENABLED=""
SCFG_TLS_CERT=""
SCFG_TLS_KEY=""
SCFG_AUTOCERT_DOMAIN=""
SCFG_AUTOCERT_CACHEDIR=""
SCFG_ALLOW_UNAUTH="false"
SCFG_SKIP_PERMS="false"
SCFG_DATA_DIR=""

# _is_loopback_host <host> -> 0 for a loopback bind.
_is_loopback_host() {
  case "$1" in
    127.0.0.1|::1|localhost) return 0 ;;
    *) return 1 ;;
  esac
}

# prompt_shared_bind — host + port (defaults from an existing config on reconfigure).
prompt_shared_bind() {
  local def_host="0.0.0.0" def_port="8080" ex
  ex="$(read_existing_config_value server.host)"; [ -n "$ex" ] && def_host="$ex"
  ex="$(read_existing_config_value server.port)"; [ -n "$ex" ] && def_port="$ex"
  SCFG_HOST="$(prompt_default BIND_HOST "Bind host" "$def_host")"
  SCFG_PORT="$(prompt_default PORT "Bind port" "$def_port")"
}

# prompt_shared_auth — edge+local (loopback-forced, single shared admin behind a
# tunnel) OR OIDC org mode.
prompt_shared_auth() {
  local model
  model="$(prompt_default AUTH_MODEL \
    "Auth model — 'edge-local' (edge auth + local mode) or 'oidc' (OIDC org mode)" \
    "edge-local")"
  case "$(printf '%s' "$model" | tr '[:upper:]' '[:lower:]')" in
    oidc|org)
      SCFG_AUTH_MODE="org"
      prompt_oidc
      ;;
    local-public)
      # Local mode but the operator intends a public bind (no edge tunnel). The
      # loopback override is skipped; the TLS step enforces the allowunauthenticated
      # escape hatch. This is the explicit P0 local+TLS+public-bind path.
      SCFG_AUTH_MODE="local"
      SCFG_PUBLIC_BASE_URL="$(prompt_default PUBLIC_BASE_URL \
        "Public base URL (https://host)" "$SCFG_PUBLIC_BASE_URL")"
      ;;
    *)
      # edge-local: force loopback bind so Arbitron is reachable only via the
      # authenticated tunnel/proxy.
      SCFG_AUTH_MODE="local"
      if ! _is_loopback_host "$SCFG_HOST"; then
        log_warn "edge+local forces a loopback bind (reachable only via your tunnel/proxy); overriding host '$SCFG_HOST' -> 127.0.0.1"
      fi
      SCFG_HOST="127.0.0.1"
      SCFG_PUBLIC_BASE_URL="$(prompt_default PUBLIC_BASE_URL \
        "Public base URL the tunnel/proxy exposes (https://host)" "$SCFG_PUBLIC_BASE_URL")"
      SCFG_COOKIE_SECURE="true"
      ;;
  esac
}

# prompt_oidc — OIDC sub-questions + derived redirect URL + optional RBAC.
prompt_oidc() {
  SCFG_OIDC_ISSUER="$(prompt_default OIDC_ISSUER "OIDC issuer URL" "$SCFG_OIDC_ISSUER")"
  SCFG_OIDC_CLIENT_ID="$(prompt_default OIDC_CLIENT_ID "OIDC client ID" "$SCFG_OIDC_CLIENT_ID")"
  SCFG_OIDC_CLIENT_SECRET="$(prompt_default OIDC_CLIENT_SECRET "OIDC client secret (stored in arbitron.env)" "")"
  if [ -z "${SCFG_OIDC_CLIENT_SECRET:-}" ]; then
    die "OIDC mode requires a non-empty client secret. Re-run with ARBITRON_OIDC_CLIENT_SECRET set."
  fi
  SCFG_PUBLIC_BASE_URL="$(prompt_default PUBLIC_BASE_URL "Public base URL (https://host)" "$SCFG_PUBLIC_BASE_URL")"
  SCFG_BOOTSTRAP_ADMINS="$(prompt_default OIDC_BOOTSTRAP_ADMINS "Bootstrap admins (comma-separated subjects/emails)" "")"
  local derived="${SCFG_PUBLIC_BASE_URL%/}/auth/callback"
  SCFG_OIDC_REDIRECT="$(prompt_default OIDC_REDIRECT "OIDC redirect URL" "$derived")"
  if [ "$SCFG_OIDC_REDIRECT" != "$derived" ]; then
    log_warn "OIDC redirect URL '$SCFG_OIDC_REDIRECT' differs from the derived '$derived'; ensure it is registered with your IdP"
  fi
  if prompt_yes_no RBAC "Enable RBAC (departments / group claims)?" no; then
    SCFG_RBAC="true"
    SCFG_RBAC_GROUP_CLAIM="$(prompt_default RBAC_GROUP_CLAIM "RBAC group claim" "groups")"
    SCFG_RBAC_DEFAULT_GROUP="$(prompt_default RBAC_DEFAULT_GROUP "RBAC default group" "default")"
    SCFG_RBAC_LEADS="$(prompt_default RBAC_LEADS "Department leads (dept=a,b;dept2=c) or empty" "")"
  else
    SCFG_RBAC="false"
  fi
}

# prompt_shared_tls — tunnel/proxy | direct cert/key | autocert, with the P0
# fail-fast invariants encoded as prompt guards so the operator never hits a
# load-time validation error.
prompt_shared_tls() {
  local model
  model="$(prompt_default TLS_MODEL \
    "TLS model — 'tunnel' (proxy terminates TLS), 'direct' (cert+key) or 'autocert'" \
    "tunnel")"
  case "$(printf '%s' "$model" | tr '[:upper:]' '[:lower:]')" in
    direct)
      SCFG_TLS_ENABLED="true"
      SCFG_TLS_CERT="$(prompt_default TLS_CERT "TLS certificate file (absolute path)" "$SCFG_TLS_CERT")"
      SCFG_TLS_KEY="$(prompt_default TLS_KEY "TLS key file (absolute path)" "$SCFG_TLS_KEY")"
      # P0: cert WITHOUT key (or vice versa) is rejected at load — guard here.
      if [ -z "$SCFG_TLS_CERT" ] || [ -z "$SCFG_TLS_KEY" ]; then
        die "direct TLS requires BOTH a certificate and a key file (cert without key is rejected by the server)"
      fi
      SCFG_AUTOCERT_DOMAIN=""
      _guard_local_tls_public_bind
      ;;
    autocert)
      SCFG_TLS_ENABLED="true"
      SCFG_AUTOCERT_DOMAIN="$(prompt_default AUTOCERT_DOMAIN "Autocert domain (Let's Encrypt)" "$SCFG_AUTOCERT_DOMAIN")"
      [ -n "$SCFG_AUTOCERT_DOMAIN" ] || die "autocert requires a domain"
      SCFG_AUTOCERT_CACHEDIR="$(prompt_default AUTOCERT_CACHEDIR "Autocert cache dir" "$ARB_LIB/autocert")"
      SCFG_TLS_CERT=""
      SCFG_TLS_KEY=""
      # P0: autocert provisions on the standard HTTPS port — force 443.
      if [ "$SCFG_PORT" != "443" ]; then
        log_warn "autocert requires port 443; overriding port '$SCFG_PORT' -> 443"
        SCFG_PORT="443"
      fi
      _guard_local_tls_public_bind
      ;;
    *)
      # tunnel/proxy: the operator runs their own TLS-terminating tunnel/proxy.
      SCFG_TLS_ENABLED="false"
      SCFG_COOKIE_SECURE="true"
      if [ -z "$SCFG_PUBLIC_BASE_URL" ]; then
        SCFG_PUBLIC_BASE_URL="$(prompt_default PUBLIC_BASE_URL "Public base URL the tunnel/proxy exposes (https://host)" "")"
      fi
      log_info "TLS is terminated by your tunnel/proxy (e.g. cloudflared); the installer never provisions it."
      log_info "  Point your tunnel/proxy at http://${SCFG_HOST}:${SCFG_PORT} and expose ${SCFG_PUBLIC_BASE_URL}."
      ;;
  esac
}

# _guard_local_tls_public_bind — P0 escape hatch: local mode (no auth) + TLS on a
# NON-loopback host exposes an unauthenticated admin API behind TLS. Require an
# explicit extra confirmation, then set allowunauthenticated=true; otherwise die.
_guard_local_tls_public_bind() {
  [ "$SCFG_AUTH_MODE" = "local" ] || return 0
  [ "$SCFG_TLS_ENABLED" = "true" ] || return 0
  _is_loopback_host "$SCFG_HOST" && return 0
  log_warn "local mode has NO authentication; enabling TLS on the non-loopback host '$SCFG_HOST'"
  log_warn "exposes an UNAUTHENTICATED admin API behind TLS."
  if prompt_yes_no ALLOW_UNAUTH "Expose the unauthenticated admin API behind TLS anyway?" no; then
    SCFG_ALLOW_UNAUTH="true"
  else
    die "refusing local-mode TLS on a public bind; bind loopback, use OIDC, or confirm the allowunauthenticated override"
  fi
}

# prompt_shared_skip_permissions — the explicit --dangerously-skip-permissions /
# RCE-warning prompt (reuses P1's wording verbatim for parity).
prompt_shared_skip_permissions() {
  log_warn "Skip tool-permission prompts enables --dangerously-skip-permissions:"
  log_warn "  the agent runs ANY tool (Bash/Write/Edit) with no human approval."
  log_warn "  This is RCE-equivalent (remote code execution) whenever a prompt input is attacker-influenced."
  if prompt_yes_no SKIP_PERMS "Enable unattended agent tools (skip permissions)?" no; then
    SCFG_SKIP_PERMS="true"
  else
    SCFG_SKIP_PERMS="false"
  fi
}

# prompt_shared_service_account — service account name; exported as ARB_SVC_USER
# so Task 1's account/ownership steps target it.
prompt_shared_service_account() {
  local user
  user="$(prompt_default SVC_USER "Service account name" "${ARB_SVC_USER:-arbitron}")"
  ARB_SVC_USER="$user"
  export ARB_SVC_USER
}

# prompt_shared_data_location — data dir (default /var/lib/arbitron).
prompt_shared_data_location() {
  SCFG_DATA_DIR="$(prompt_default DATA_DIR "Data location" "$ARB_LIB")"
}

# run_shared_qa — the canonical shared Q&A in order (bind -> auth -> tls ->
# skip-perms -> service account -> data location). Also resolves LLM access /
# API key (reusing the P1 CFG_* convention) for the secrets env.
run_shared_qa() {
  prompt_shared_bind
  prompt_shared_auth
  prompt_shared_tls
  prompt_shared_skip_permissions
  prompt_shared_service_account
  prompt_shared_data_location

  CFG_LLM_ACCESS="$(prompt_default LLM_ACCESS \
    "LLM access — 'subscription' (Claude login) or 'api-key' (Anthropic key)" \
    "subscription")"
  if [ "$CFG_LLM_ACCESS" = "api-key" ]; then
    CFG_API_KEY="$(prompt_default API_KEY "Paste your Anthropic API key" "")"
    if [ -z "${CFG_API_KEY:-}" ]; then
      die "api-key mode requires a non-empty Anthropic API key. Re-run with ARBITRON_API_KEY set or choose 'subscription'."
    fi
  fi
}

# write_shared_config — emit /etc/arbitron/config.yaml using the exact koanf keys
# from adapter/config/loader.go. NO secrets inside (the OIDC client secret, API
# key and master key all live in arbitron.env). Absolute system paths only.
write_shared_config() {
  local cfg data
  cfg="$(layout_config)"
  data="${SCFG_DATA_DIR:-$ARB_LIB}"

  local provider llm_block
  if [ "${CFG_LLM_ACCESS:-subscription}" = "api-key" ]; then
    provider="anthropic"
    llm_block="  provider: \"anthropic\""
  else
    provider="claude-cli"
    llm_block="  provider: \"claude-cli\"
  binarypath: \"${ARBITRON_CLAUDE_PATH:-}\""
  fi
  : "$provider"

  {
    printf '# Arbitron config (generated by install.sh, shared profile). Secret-free.\n'
    printf 'server:\n'
    printf '  host: "%s"\n' "${SCFG_HOST:-0.0.0.0}"
    printf '  port: %s\n' "${SCFG_PORT:-8080}"
    [ -n "$SCFG_PUBLIC_BASE_URL" ] && printf '  publicbaseurl: "%s"\n' "$SCFG_PUBLIC_BASE_URL"
    printf '  tls:\n'
    printf '    enabled: %s\n' "${SCFG_TLS_ENABLED:-false}"
    printf '    certfile: "%s"\n' "${SCFG_TLS_CERT:-}"
    printf '    keyfile: "%s"\n' "${SCFG_TLS_KEY:-}"
    printf '    autocertdomain: "%s"\n' "${SCFG_AUTOCERT_DOMAIN:-}"
    printf '    autocertcachedir: "%s"\n' "${SCFG_AUTOCERT_CACHEDIR:-}"
    printf '    allowunauthenticated: %s\n' "${SCFG_ALLOW_UNAUTH:-false}"
    printf 'database:\n'
    printf '  path: "%s/arbitron.db"\n' "$data"
    printf 'workspace:\n'
    printf '  path: "%s/workspaces"\n' "$data"
    printf 'llm:\n'
    printf '%s\n' "$llm_block"
    printf '  skippermissions: %s\n' "${SCFG_SKIP_PERMS:-false}"
    printf 'logging:\n'
    printf '  level: "info"\n'
    printf '  file: "%s/arbitron.log"\n' "$ARB_LOG"
    printf 'auth:\n'
    printf '  mode: "%s"\n' "${SCFG_AUTH_MODE:-local}"
    if [ -n "$SCFG_BOOTSTRAP_ADMINS" ]; then
      printf '  bootstrapadmins: "%s"\n' "$SCFG_BOOTSTRAP_ADMINS"
    fi
    if [ "$SCFG_AUTH_MODE" = "org" ]; then
      printf '  oidc:\n'
      printf '    issuerurl: "%s"\n' "$SCFG_OIDC_ISSUER"
      printf '    clientid: "%s"\n' "$SCFG_OIDC_CLIENT_ID"
      printf '    redirecturl: "%s"\n' "$SCFG_OIDC_REDIRECT"
    fi
    printf '  session:\n'
    printf '    cookiesecure: %s\n' "${SCFG_COOKIE_SECURE:-true}"
    if [ "$SCFG_RBAC" = "true" ]; then
      printf '  rbac:\n'
      printf '    groupclaim: "%s"\n' "$SCFG_RBAC_GROUP_CLAIM"
      printf '    defaultgroup: "%s"\n' "$SCFG_RBAC_DEFAULT_GROUP"
      if [ -n "$SCFG_RBAC_LEADS" ]; then
        printf '    leads:\n'
        local entry dept members
        local _ifs="$IFS"; IFS=';'
        for entry in $SCFG_RBAC_LEADS; do
          [ -n "$entry" ] || continue
          dept="${entry%%=*}"
          members="${entry#*=}"
          printf '      %s: "%s"\n' "$dept" "$members"
        done
        IFS="$_ifs"
      fi
    fi
  } >"$cfg"

  # config.yaml is world-readable (no secrets); root-owned.
  chmod 0644 "$cfg"
  chown root:root "$cfg" 2>/dev/null || true
}

# write_shared_env — emit /etc/arbitron/arbitron.env (0600 root:arbitron) with
# the silently-generated master key plus optional API key + OIDC client secret.
# Secrets never echoed.
write_shared_env() {
  local env
  env="$(layout_secrets_env)"
  local old_umask; old_umask="$(umask)"
  umask 077
  {
    printf '# Arbitron secrets — referenced by the systemd EnvironmentFile. DO NOT commit.\n'
    printf 'ARBITRON_MASTER_KEY=%s\n' "$MASTER_KEY"
    if [ "${CFG_LLM_ACCESS:-}" = "api-key" ] && [ -n "${CFG_API_KEY:-}" ]; then
      printf 'ANTHROPIC_API_KEY=%s\n' "$CFG_API_KEY"
    fi
    if [ "$SCFG_AUTH_MODE" = "org" ] && [ -n "$SCFG_OIDC_CLIENT_SECRET" ]; then
      printf 'ARBITRON_AUTH_OIDC_CLIENTSECRET=%s\n' "$SCFG_OIDC_CLIENT_SECRET"
    fi
  } >"$env"
  umask "$old_umask"
  chmod 0600 "$env"
  chown "root:${ARB_SVC_USER:-arbitron}" "$env" 2>/dev/null || true
}

# --- Task 3: systemd SYSTEM unit + health check + lifecycle -----------------

# write_systemd_unit — render the hardened SYSTEM unit. CAP_NET_BIND_SERVICE is
# added only for a privileged port (< 1024) so the non-root account can bind it.
write_systemd_unit() {
  local user="${ARB_SVC_USER:-arbitron}"
  local port="${SCFG_PORT:-${PORT:-8080}}"
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/arbitron-unit.XXXXXX")" || return 1

  {
    printf '[Unit]\n'
    printf 'Description=Arbitron\n'
    printf 'After=network-online.target\n'
    printf 'Wants=network-online.target\n\n'
    printf '[Service]\n'
    printf 'Type=simple\n'
    printf 'User=%s\n' "$user"
    printf 'Group=%s\n' "$user"
    printf 'EnvironmentFile=%s/arbitron.env\n' "$ARB_ETC"
    printf 'WorkingDirectory=%s\n' "$ARB_LIB"
    printf 'ExecStart=%s/arbitron serve --config %s/config.yaml\n' "$ARB_PREFIX_BIN" "$ARB_ETC"
    printf 'Restart=on-failure\n'
    printf 'RestartSec=5\n'
    printf '# Hardening\n'
    printf 'NoNewPrivileges=yes\n'
    printf 'ProtectSystem=strict\n'
    printf 'ProtectHome=yes\n'
    printf 'ReadWritePaths=%s %s\n' "$ARB_LIB" "$ARB_LOG"
    if [ "$port" -lt 1024 ] 2>/dev/null; then
      printf 'AmbientCapabilities=CAP_NET_BIND_SERVICE\n'
    fi
    printf '\n[Install]\n'
    printf 'WantedBy=multi-user.target\n'
  } >"$tmp"

  install -m 0644 -o root -g root "$tmp" "$ARB_SYSTEMD_UNIT_PATH"
  rm -f "$tmp"
}

# enable_systemd_unit — reload systemd then enable + start the unit at boot.
enable_systemd_unit() {
  systemctl daemon-reload
  systemctl enable --now arbitron
}

# health_check_shared — poll the configured scheme/host/port. TLS uses https +
# -k (the cert may be self-signed or autocert-pending). On failure, point at
# journalctl and the log dir.
health_check_shared() {
  local port="${SCFG_PORT:-${PORT:-8080}}"
  local host="${SCFG_HOST:-${BIND_HOST:-127.0.0.1}}"
  local tls="${SCFG_TLS_ENABLED:-${TLS_ENABLED:-false}}"
  _is_loopback_host "$host" || host="127.0.0.1"
  local scheme="http" insecure=""
  if [ "$tls" = "true" ]; then
    scheme="https"
    insecure="-k"
    host="${SCFG_HOST:-${BIND_HOST:-127.0.0.1}}"   # TLS: keep the real (cert) host
  fi
  local retries="${ARBITRON_HEALTH_RETRIES:-20}"
  local sleep_s="${ARBITRON_HEALTH_SLEEP:-1}"
  local i code
  for ((i = 1; i <= retries; i++)); do
    code="$(curl -fsS $insecure -o /dev/null -w '%{http_code}' "${scheme}://${host}:${port}/" 2>/dev/null || echo 000)"
    if [ "$code" = "200" ]; then
      log_info "health check passed (HTTP 200 on ${scheme}://${host}:${port})"
      return 0
    fi
    [ "$sleep_s" != "0" ] && sleep "$sleep_s"
  done
  log_error "health check failed after ${retries} attempts"
  log_error "  inspect the service: journalctl -u arbitron"
  log_error "  logs: $ARB_LOG"
  return 1
}

# print_shared_summary — final dashboard URL / service / log pointers.
print_shared_summary() {
  local port="${SCFG_PORT:-${PORT:-8080}}"
  log_info "Arbitron (shared) is ready."
  if [ -n "$SCFG_PUBLIC_BASE_URL" ]; then
    log_info "  Public URL: $SCFG_PUBLIC_BASE_URL"
  fi
  log_info "  Local bind: ${SCFG_HOST:-0.0.0.0}:${port}"
  log_info "  Service:    systemd unit 'arbitron' (systemctl status arbitron)"
  log_info "  Logs:       $ARB_LOG (journalctl -u arbitron)"
  log_info "  Config:     $ARB_ETC/config.yaml"
  if [ "${SCFG_TLS_ENABLED:-false}" = "false" ] && [ -n "$SCFG_PUBLIC_BASE_URL" ]; then
    log_info "  TLS is terminated by your tunnel/proxy — the installer never provisions it."
  fi
}

# run_shared_install — the Shared orchestrator (Fresh path).
run_shared_install() {
  require_root
  detect_os   >/dev/null || die "could not detect OS"
  detect_arch >/dev/null || die "could not detect architecture"
  log_info "Shared (system) install under $ARB_ETC + $ARB_LIB"

  # Q&A first so ARB_SVC_USER / data location feed the account + layout steps.
  run_shared_qa
  resolve_dependencies || die "dependency resolution failed"
  ensure_service_account
  create_system_layout
  install_binary_system || die "binary download failed; nothing was installed"

  generate_master_key
  write_shared_config
  write_shared_env

  write_systemd_unit
  enable_systemd_unit
  health_check_shared || die "service did not become healthy"
  print_shared_summary
}

# upgrade_shared — swap the binary + restart; preserve config/data/account.
upgrade_shared() {
  require_root
  log_info "Upgrading Arbitron (config, data and secrets are preserved)"
  install_binary_system || die "binary download failed; the existing install is unchanged"
  systemctl restart arbitron
  health_check_shared "$(read_existing_config_value server.port || echo 8080)" || true
  log_warn "DB migrations run forward-only on serve start; there is no automatic"
  log_warn "down-migration. Review the release notes before downgrading the binary."
  print_shared_summary
}

# reconfigure_shared — re-run the Q&A (pre-filled), rewrite config, restart.
# Preserves the master key + /var/lib data.
reconfigure_shared() {
  require_root
  log_info "Reconfiguring Arbitron (data and master key are preserved)"
  run_shared_qa
  generate_master_key   # detects + reuses the existing key
  write_shared_config
  write_shared_env
  systemctl restart arbitron
  health_check_shared || true
  print_shared_summary
}

# uninstall_shared — stop/disable/remove unit + binary + config; prompt before
# removing data (default keep) and the service account (default keep).
uninstall_shared() {
  require_root
  log_info "Uninstalling Arbitron (shared)"
  systemctl disable --now arbitron >/dev/null 2>&1 || true
  rm -f "$ARB_SYSTEMD_UNIT_PATH"
  systemctl daemon-reload >/dev/null 2>&1 || true
  rm -f "$ARB_PREFIX_BIN/arbitron"
  rm -f "$ARB_ETC/config.yaml" "$ARB_ETC/arbitron.env"
  if prompt_yes_no REMOVE_DATA "Also remove all data ($ARB_LIB)? This is irreversible." no; then
    rm -rf "$ARB_LIB"
    log_info "removed data at $ARB_LIB"
  else
    log_info "kept your data at $ARB_LIB"
  fi
  if prompt_yes_no REMOVE_ACCOUNT "Also remove the '${ARB_SVC_USER:-arbitron}' service account?" no; then
    userdel "${ARB_SVC_USER:-arbitron}" >/dev/null 2>&1 || log_warn "could not remove the service account"
    log_info "removed the service account"
  else
    log_info "kept the '${ARB_SVC_USER:-arbitron}' service account"
  fi
  log_info "Arbitron uninstalled"
}

# ---------------------------------------------------------------------------
# Service registration & lifecycle (launchd / systemd --user)
#
# User-level, reboot-persistent service for the two local platforms. Every unit
# invokes `arbitron serve --config <abs path>` (the P0 flag) so the service finds
# the right config regardless of the run-as home. NO sudo, no dedicated account
# (that is P2). P2 reuses the unit-render + lifecycle seams.
# ---------------------------------------------------------------------------

_launchd_plist_path() { printf '%s/Library/LaunchAgents/%s.plist\n' "$HOME" "$ARBITRON_LAUNCHD_LABEL"; }
_systemd_unit_path()  { printf '%s/.config/systemd/user/%s.service\n' "$HOME" "$ARBITRON_SYSTEMD_UNIT"; }

# render_launchd_plist — write the LaunchAgent plist. Secret delivery: launchd
# loads ARBITRON_CONFIG (the config path) directly; the master key / API key are
# sourced by the binary from the secrets env via a tiny wrapper rather than being
# duplicated in plaintext in the plist. The wrapper sources arbitron.env then
# exec's the binary, keeping secrets out of the (world-readable-ish) plist.
render_launchd_plist() {
  local plist wrapper bin cfg logs
  plist="$(_launchd_plist_path)"
  bin="$(layout_bin)/arbitron"
  cfg="$(layout_config)"
  logs="$(layout_logs)"
  wrapper="$(layout_bin)/arbitron-service"

  mkdir -p "$(dirname "$plist")" "$(layout_bin)" "$logs"

  # Wrapper sources the 0600 secrets env, then exec's the binary. This keeps the
  # master key out of the plist.
  cat >"$wrapper" <<EOF
#!/bin/sh
set -e
if [ -f "$(layout_secrets_env)" ]; then
  set -a
  . "$(layout_secrets_env)"
  set +a
fi
exec "$bin" serve --config "$cfg"
EOF
  chmod 0755 "$wrapper"

  cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${ARBITRON_LAUNCHD_LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${wrapper}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ARBITRON_CONFIG</key>
    <string>${cfg}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${logs}/arbitron.out.log</string>
  <key>StandardErrorPath</key>
  <string>${logs}/arbitron.err.log</string>
</dict>
</plist>
EOF
}

# register_service_macos — write the plist and (re)load it via launchctl.
register_service_macos() {
  render_launchd_plist
  local plist uid
  plist="$(_launchd_plist_path)"
  uid="$(_uid)"
  # Tear down any prior instance, then bootstrap + kickstart.
  launchctl bootout "gui/${uid}/${ARBITRON_LAUNCHD_LABEL}" >/dev/null 2>&1 || true
  if ! launchctl bootstrap "gui/${uid}" "$plist" 2>/dev/null; then
    # Fallback for older launchd.
    launchctl load -w "$plist"
  fi
  launchctl kickstart -k "gui/${uid}/${ARBITRON_LAUNCHD_LABEL}" >/dev/null 2>&1 || true
}

# render_systemd_unit — write the systemd --user unit.
render_systemd_unit() {
  local unit bin cfg data
  unit="$(_systemd_unit_path)"
  bin="$(layout_bin)/arbitron"
  cfg="$(layout_config)"
  data="${CFG_DATA_DIR:-$(layout_data)}"
  mkdir -p "$(dirname "$unit")" "$data"

  cat >"$unit" <<EOF
[Unit]
Description=Arbitron workflow engine
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${bin} serve --config ${cfg}
EnvironmentFile=$(layout_secrets_env)
WorkingDirectory=${data}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
}

# register_service_linux — write the unit, reload, enable+start, enable linger.
register_service_linux() {
  render_systemd_unit
  systemctl --user daemon-reload
  systemctl --user enable --now "$ARBITRON_SYSTEMD_UNIT"
  loginctl enable-linger "$USER" || log_warn "could not enable linger; service may not survive logout"
}

# register_service — dispatch on OS.
register_service() {
  local os
  os="$(detect_os)" || return 1
  case "$os" in
    macos) register_service_macos ;;
    linux) register_service_linux ;;
    *)     log_error "no service backend for $os"; return 1 ;;
  esac
}

# Per-OS lifecycle predicates / actions.
service_present() {
  local os
  os="$(detect_os)" || return 1
  case "$os" in
    macos) [ -f "$(_launchd_plist_path)" ] ;;
    linux) [ -f "$(_systemd_unit_path)" ] ;;
    *)     return 1 ;;
  esac
}

service_restart() {
  local os uid
  os="$(detect_os)" || return 1
  case "$os" in
    macos)
      uid="$(_uid)"
      launchctl kickstart -k "gui/${uid}/${ARBITRON_LAUNCHD_LABEL}" ;;
    linux)
      systemctl --user restart "$ARBITRON_SYSTEMD_UNIT" ;;
  esac
}

service_stop() {
  local os uid
  os="$(detect_os)" || return 1
  case "$os" in
    macos)
      uid="$(_uid)"
      launchctl bootout "gui/${uid}/${ARBITRON_LAUNCHD_LABEL}" >/dev/null 2>&1 || true ;;
    linux)
      systemctl --user stop "$ARBITRON_SYSTEMD_UNIT" >/dev/null 2>&1 || true ;;
  esac
}

service_remove() {
  local os uid plist unit
  os="$(detect_os)" || return 1
  case "$os" in
    macos)
      uid="$(_uid)"
      plist="$(_launchd_plist_path)"
      launchctl bootout "gui/${uid}/${ARBITRON_LAUNCHD_LABEL}" >/dev/null 2>&1 || true
      rm -f "$plist" "$(layout_bin)/arbitron-service" ;;
    linux)
      unit="$(_systemd_unit_path)"
      systemctl --user disable --now "$ARBITRON_SYSTEMD_UNIT" >/dev/null 2>&1 || true
      rm -f "$unit"
      systemctl --user daemon-reload >/dev/null 2>&1 || true ;;
  esac
}

# health_check <port> — bounded retry loop curling the local dashboard. Success
# on a 200; on persistent failure, die with a pointer to the log dir.
health_check() {
  local port="$1"
  local retries="${ARBITRON_HEALTH_RETRIES:-20}"
  local sleep_s="${ARBITRON_HEALTH_SLEEP:-1}"
  local i code
  for ((i = 1; i <= retries; i++)); do
    code="$(curl -fsS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/" 2>/dev/null || echo 000)"
    if [ "$code" = "200" ]; then
      log_info "health check passed (HTTP 200 on :${port})"
      return 0
    fi
    [ "$sleep_s" != "0" ] && sleep "$sleep_s"
  done
  log_error "health check failed after ${retries} attempts; see logs at $(layout_logs)"
  return 1
}

# ---------------------------------------------------------------------------
# Existing-install detection & state dispatch
# ---------------------------------------------------------------------------

# has_existing_config -> 0 if config.yaml exists.
has_existing_config() {
  [ -f "$(layout_config)" ]
}

# detect_state -> Fresh | Upgrade | Reconfigure | Uninstall.
#   - With no existing install -> Fresh (unless ARBITRON_ACTION forces otherwise).
#   - ARBITRON_ACTION (upgrade|reconfigure|uninstall|fresh) takes precedence.
#   - With an existing install and no action, prompt interactively.
detect_state() {
  local action="${ARBITRON_ACTION:-}"
  if [ -n "$action" ]; then
    case "$(printf '%s' "$action" | tr '[:upper:]' '[:lower:]')" in
      fresh)       printf 'Fresh\n';       return 0 ;;
      upgrade)     printf 'Upgrade\n';     return 0 ;;
      reconfigure) printf 'Reconfigure\n'; return 0 ;;
      uninstall)   printf 'Uninstall\n';   return 0 ;;
      *)           log_error "unknown ARBITRON_ACTION '$action' (want fresh|upgrade|reconfigure|uninstall)"; return 1 ;;
    esac
  fi

  if has_existing_config || has_existing_service; then
    # Existing install with no explicit action -> ask what to do.
    local choice
    choice="$(prompt_default ACTION 'Existing Arbitron install found. Action? (upgrade/reconfigure/uninstall)' 'upgrade')"
    case "$(printf '%s' "$choice" | tr '[:upper:]' '[:lower:]')" in
      upgrade)     printf 'Upgrade\n' ;;
      reconfigure) printf 'Reconfigure\n' ;;
      uninstall)   printf 'Uninstall\n' ;;
      *)           log_error "unknown action '$choice'"; return 1 ;;
    esac
    return 0
  fi

  printf 'Fresh\n'
}

# has_existing_service — true when a service unit is registered (per-OS).
has_existing_service() {
  service_present
}

# ---------------------------------------------------------------------------
# Lifecycle handlers
# ---------------------------------------------------------------------------

# run_fresh — full fresh install: deps -> Q&A -> master key -> binary ->
# config + secrets -> service -> health check -> summary.
run_fresh() {
  log_info "Fresh install under $(arbitron_home)"
  run_config_qa
  resolve_dependencies || die "dependency resolution failed"
  generate_master_key
  download_binary || die "binary download failed; nothing was installed"
  write_config_yaml
  write_secrets_env
  claude_login
  register_service || die "service registration failed"
  health_check "${CFG_PORT:-8080}" || die "service did not become healthy"
  print_summary
}

# run_upgrade — download+verify the new binary, swap it in, restart, health-check.
# PRESERVES config + data + secrets. Surfaces the forward-only migration caveat.
run_upgrade() {
  log_info "Upgrading Arbitron (config, data and secrets are preserved)"
  download_binary || die "binary download failed; the existing install is unchanged"
  service_restart
  health_check "$(read_existing_config_value server.port || echo 8080)"
  log_warn "DB migrations run forward-only on serve start; there is no automatic"
  log_warn "down-migration. Review the release notes before downgrading the binary."
  print_summary
}

# run_reconfigure — re-run the Q&A pre-filled, rewrite config, restart. Does NOT
# touch the DB/data or regenerate the master key.
run_reconfigure() {
  log_info "Reconfiguring Arbitron (data and master key are preserved)"
  run_config_qa
  generate_master_key   # detects + reuses the existing key
  write_config_yaml
  write_secrets_env
  service_restart
  health_check "${CFG_PORT:-8080}"
  print_summary
}

# run_uninstall — stop + remove service, binary, config; prompt for data removal
# (default KEEP).
run_uninstall() {
  log_info "Uninstalling Arbitron"
  service_stop
  service_remove
  rm -f "$(layout_bin)/arbitron" "$(layout_config)"
  if prompt_yes_no REMOVE_DATA "Also remove all data (DB, workspaces, logs)? This is irreversible." no; then
    rm -rf "$(layout_data)" "$(layout_workspaces)" "$(layout_logs)" "$(layout_secrets_env)"
    log_info "removed data, workspaces, logs and secrets"
  else
    log_info "kept your data at $(layout_data) (and secrets at $(layout_secrets_env))"
  fi
  log_info "Arbitron uninstalled"
}

# print_summary — final dashboard URL / service / log pointer + next steps.
print_summary() {
  local port os svc
  port="${CFG_PORT:-$(read_existing_config_value server.port || echo 8080)}"
  os="$(detect_os 2>/dev/null || echo unknown)"
  case "$os" in
    macos) svc="launchd agent ${ARBITRON_LAUNCHD_LABEL} (launchctl)" ;;
    linux) svc="systemd --user unit ${ARBITRON_SYSTEMD_UNIT} (systemctl --user)" ;;
    *)     svc="(service)" ;;
  esac
  log_info "Arbitron is ready."
  log_info "  Dashboard: http://127.0.0.1:${port}"
  log_info "  Service:   ${svc}"
  log_info "  Logs:      $(layout_logs)"
  log_info "  Config:    $(layout_config)"
}

# ---------------------------------------------------------------------------
# main dispatcher
# ---------------------------------------------------------------------------

main() {
  local os arch itype state
  os="$(detect_os)"   || die "could not detect OS"
  arch="$(detect_arch)" || die "could not detect architecture"
  itype="$(select_install_type)" || die "could not select install type"
  log_info "Arbitron installer — os=$os arch=$arch type=$itype"

  if [ "$itype" = "Shared" ]; then
    # System-wide install: switch the layout resolvers to the system paths and
    # dispatch the shared lifecycle. require_root (inside each handler) enforces
    # elevation before anything is written.
    ARB_INSTALL_MODE="shared"
    state="$(detect_state)" || die "could not determine install action"
    log_info "action: $state"
    case "$state" in
      Fresh)       run_shared_install ;;
      Upgrade)     upgrade_shared ;;
      Reconfigure) reconfigure_shared ;;
      Uninstall)   uninstall_shared ;;
      *)           die "unhandled state: $state" ;;
    esac
    return
  fi

  if is_elevated; then
    log_warn "running as root; the local profile installs per-user under ~/.arbitron"
  fi

  state="$(detect_state)" || die "could not determine install action"
  log_info "action: $state"

  case "$state" in
    Fresh)       run_fresh ;;
    Upgrade)     run_upgrade ;;
    Reconfigure) run_reconfigure ;;
    Uninstall)   run_uninstall ;;
    *)           die "unhandled state: $state" ;;
  esac
}

# ===========================================================================
# Library guard: when ARBITRON_INSTALL_LIB_ONLY is set, return after defining
# functions WITHOUT running main (enables `source install.sh` from bats).
# ===========================================================================
if [ -n "${ARBITRON_INSTALL_LIB_ONLY:-}" ]; then
  # shellcheck disable=SC2317  # reached only when sourced; `return` is valid there.
  return 0 2>/dev/null
fi

main "$@"
