#!/bin/sh
# Runs ONE command of repository-controlled code (a mutation tool, a test run, a
# version probe) behind an enforced per-run boundary. harden's detect, mutate and
# rescore stages call it for every such launch; clone, push and manifest steps
# stay outside it, so the credentials those need never enter the boundary.
#
#   sandbox.sh --run-dir <instance workspace> [--cwd <dir>] [--rw <path>]...
#              [--ro <path>]... [--env NAME=VALUE]... -- <command> [args...]
#   sandbox.sh --check                      (exit 0 only if a sandbox can be built)
#
# What the command gets:
#   environment  none of the caller's: `env -i` with the caller's PATH (absolute
#                entries that exist), a scratch HOME and
#                TMPDIR, a fixed locale/toolchain allowlist, and the --env pairs.
#                Tokens, HOME, CLAUDE_CONFIG_DIR, SSH_AUTH_SOCK and the rest of
#                the service environment never reach it, and this script drops
#                them from its own process before it starts the command. On macOS
#                other same-user processes' environments stay readable (see the
#                relaunch below), so a token must not sit in any of them, and
#                Arbitron's agent subprocesses do hold provider keys: see "macOS
#                hosts" in samples/harden/README.md for the exposure and the
#                mitigation (Linux, or a dedicated user).
#   filesystem   read-only toolchain (PATH, system directories, the dependency
#                caches listed below), read-write only the --rw paths and its
#                scratch directory. The instance workspace is readable (it is
#                this run's), but the host HOME, other users' homes, the
#                workspace root and every sibling run are not, and a --rw path
#                that is a git work tree has its .git (and, for a linked
#                worktree, the common git dir) read-only, so repository code
#                cannot plant a hook or repoint .git for the trusted git calls
#                that follow.
#   network      loopback only. No route off the host: no registry, no cloud
#                metadata service. An operator may set ARBITRON_HARDEN_NETWORK=allow
#                in the SERVICE environment to lift this; nothing a workflow or
#                repository supplies can.
#
# Enforcement: bubblewrap (mount, pid and network namespaces) on Linux,
# sandbox-exec (Seatbelt) on macOS. FAILS CLOSED: with neither, or when the
# sandbox cannot be created (user namespaces disabled, a profile the kernel
# rejects), nothing runs and the exit status is 125 with a message saying why.
# Any other status is the command's own.
#
# Operator extensions, from the service environment only (never from workflow
# input): HARDEN_SANDBOX_RO and HARDEN_SANDBOX_RW are colon-separated extra
# read-only / read-write paths, and HARDEN_SANDBOX_PASS is a space-separated list
# of environment variable names to pass through (for toolchain locators).
set -eu

die() { echo "sandbox: $*" >&2; exit 125; }

# --- own environment -------------------------------------------------------------
# Relaunch this script with a scrubbed environment before anything else. macOS
# hands any same-user process the exec-time environment of every non-platform
# process through sysctl(KERN_PROCARGS2), and Seatbelt does not mediate that
# sysctl (nor does sysctl-read, process-info* or a deny-default profile stop it).
# So `env -i` for the command alone left the caller's tokens readable one pid up
# whenever this script ran under a non-Apple shell. Only the service knobs read
# below, the locale/toolchain allowlist and the operator's pass-through names
# survive the relaunch.
#
# The relaunched copy is marked by a leading argv sentinel, and it VERIFIES that
# its environment holds nothing else, so neither a forged sentinel nor any
# variable a workflow can set skips the scrub: a dirty environment there fails
# closed. Every way the relaunch itself can fail is checked first, so it exits
# 125 like any other isolation failure rather than with exec's 126/127.
RELAUNCHED=--sandbox-relaunched-scrubbed

# pass_names: the operator's pass-through names, space-separated. Never globbed,
# and only shell identifiers: a name like "1" would expand a positional parameter.
pass_names() {
  set -f
  for _v in ${HARDEN_SANDBOX_PASS:-}; do
    case "$_v" in [A-Za-z_]*) ;; *) continue ;; esac
    case "$_v" in *[!A-Za-z0-9_]*) continue ;; esac
    printf '%s ' "$_v"
  done
  set +f
}
KEEP_NAMES="PATH HOME LANG LC_ALL LC_CTYPE TZ JAVA_HOME DOTNET_ROOT GOROOT RUSTUP_HOME HARDEN_SANDBOX_RO HARDEN_SANDBOX_RW HARDEN_SANDBOX_PASS ARBITRON_HARDEN_NETWORK $(pass_names)"

# goroot_ok <dir>: a resolved GOROOT is an absolute, existing directory with an
# executable bin/go, and carries no newline or ':' (it becomes a PATH entry, a
# profile path and an env.pairs line). Prints why not.
goroot_ok() {
  case "$1" in
    /*) ;;
    *) echo "'$1' is not an absolute path"; return 1 ;;
  esac
  case "$1" in
    *"$NL"*|*:*) echo "'$1' holds a newline or ':'"; return 1 ;;
  esac
  { [ -d "$1" ] && [ -x "$1/bin/go" ]; } || { echo "'$1' is not a Go toolchain (no bin/go)"; return 1; }
}
NL='
'

ENV_BIN="$(command -v env)" && [ -x "$ENV_BIN" ] || die "no isolation available: env is required to scrub the environment; refusing to run repository code unisolated"
if [ "${1:-}" = "$RELAUNCHED" ]; then
  GO_ROOT="${2:-}"
  shift 2 || die "the relaunch lost its arguments"
  # Exported names only (a multi-line value's continuation lines can only add
  # names, which fails closed). The shell itself exports PWD, SHLVL, OLDPWD, _.
  _allowed=" $KEEP_NAMES PWD SHLVL OLDPWD _ "
  "$ENV_BIN" | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' | while IFS= read -r _n; do
    case "$_allowed" in *" $_n "*) ;; *) echo "$_n"; exit 1 ;; esac
  done > /dev/null || die "the environment was not scrubbed before the relaunch; refusing to run repository code"
  # The outer copy validated it already; a forged relaunch must not skip that.
  if [ -n "$GO_ROOT" ]; then
    _why="$(goroot_ok "$GO_ROOT")" || die "refusing the resolved Go toolchain: $_why"
  fi
else
  SH_BIN="$(command -v sh)" && [ -x "$SH_BIN" ] || die "no isolation available: sh is not on PATH; refusing to run repository code unisolated"
  [ -f "$0" ] && [ -r "$0" ] || die "cannot relaunch: $0 is not a readable script file (run sandbox.sh by path); refusing to run repository code unisolated"
  # Go toolchain. A host's base go may be older than the module's go directive
  # and reach the right release only through GOTOOLCHAIN=auto (a download, or a
  # toolchain module in the cache checked against the sum database), which the
  # closed sandbox cannot do. So for a Go module it is resolved HERE, outside the
  # sandbox, in the command's directory and with the caller's own Go settings;
  # the command then runs that GOROOT (read-only) with GOTOOLCHAIN=local.
  #
  # This is the one step that runs out here, so it must not run the repository's
  # code: `go` is looked up ONCE, before any cd, among the ABSOLUTE PATH entries
  # only (an empty, "." or "bin" entry would find a file the repository ships
  # once inside the work tree), a go inside the run's workspace is refused, and
  # it runs by that path with a PATH of absolute entries outside the workspace
  # (go itself looks up goX.Y.Z and git there). A failed or bogus answer refuses.
  GO_ROOT=""
  _cwd=""; _rd=""; _prev=""
  for _a in "$@"; do
    case "$_prev" in
      --cwd) _cwd="$_a"; _prev=""; continue ;;
      --run-dir) _rd="$_a"; _prev=""; continue ;;
      --rw|--ro|--env) _prev=""; continue ;;
    esac
    [ "$_a" = -- ] && break
    _prev="$_a"
  done
  _ws=""
  [ -n "$_rd" ] && [ -d "$_rd" ] && _ws="$(cd "$_rd" && pwd -P)"
  # in_ws <dir>: the physical dir is the run's workspace or lies under it.
  in_ws() {
    [ -n "$_ws" ] || return 1
    _p="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
    case "$_p" in "$_ws"|"$_ws"/*) return 0 ;; esac
    return 1
  }
  GO_BIN=""; GO_PATH=""
  _oifs="$IFS"; IFS=:
  set -f
  for _e in ${PATH:-}; do
    case "$_e" in /*) ;; *) continue ;; esac
    [ -d "$_e" ] || continue
    if [ -z "$GO_BIN" ] && [ -f "$_e/go" ] && [ -x "$_e/go" ]; then GO_BIN="$_e/go"; GO_BIN_DIR="$_e"; fi
    in_ws "$_e" || GO_PATH="${GO_PATH:+$GO_PATH:}$_e"
  done
  set +f; IFS="$_oifs"
  if [ -n "$GO_BIN" ]; then
    _dir="${_cwd:-$_rd}"
    _mod=""
    if [ -n "$_dir" ] && [ -d "$_dir" ]; then
      _d="$(cd "$_dir" && pwd -P)"
      while :; do
        if [ -f "$_d/go.mod" ] || [ -f "$_d/go.work" ]; then _mod=1; break; fi
        [ "$_d" = / ] && break
        _d="$(dirname "$_d")"
      done
    fi
    if [ -n "$_mod" ]; then
      ! in_ws "$GO_BIN_DIR" \
        || die "the go on PATH ($GO_BIN) is inside the run's workspace; refusing to run repository code outside the sandbox"
      GO_ROOT="$(cd "$_dir" && PATH="$GO_PATH" "$GO_BIN" env GOROOT < /dev/null)" \
        || die "cannot resolve the Go toolchain for $_dir (go env GOROOT failed, see above); refusing to run it with the wrong one"
      _why="$(goroot_ok "$GO_ROOT")" || die "cannot resolve the Go toolchain for $_dir: $_why"
      GO_ROOT="$(cd "$GO_ROOT" && pwd -P)"
    fi
  fi
  n=$#
  set -- "$@" -i
  for v in $KEEP_NAMES; do
    isset=""
    eval "isset=\${$v+x}"
    if [ "$isset" = x ]; then eval "set -- \"\$@\" \"$v=\${$v}\""; fi
  done
  set -- "$@" "$SH_BIN" "$0" "$RELAUNCHED" "$GO_ROOT"
  # Rotate the original arguments behind the relaunch command.
  while [ "$n" -gt 0 ]; do set -- "$@" "$1"; shift; n=$((n - 1)); done
  exec "$ENV_BIN" "$@"
fi

RUN_DIR=""
CWD=""
RW=""
RO=""
ENVS=""
CHECK=0
NL='
'
while [ $# -gt 0 ]; do
  case "$1" in
    --run-dir) [ $# -ge 2 ] || die "--run-dir needs a value"; RUN_DIR="$2"; shift ;;
    --cwd) [ $# -ge 2 ] || die "--cwd needs a value"; CWD="$2"; shift ;;
    --rw) [ $# -ge 2 ] || die "--rw needs a value"; RW="$RW$2$NL"; shift ;;
    --ro) [ $# -ge 2 ] || die "--ro needs a value"; RO="$RO$2$NL"; shift ;;
    --env) [ $# -ge 2 ] || die "--env needs a value"; ENVS="$ENVS$2$NL"; shift ;;
    --check) CHECK=1 ;;
    --) shift; break ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

# --- argument assembly ----------------------------------------------------------
# Arguments are accumulated as a shell-quoted string and expanded once with eval,
# so values holding spaces or quotes survive in POSIX sh.
q() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
ARGV=""
add() { for _a in "$@"; do ARGV="$ARGV $(q "$_a")"; done; }

# --- mechanism ----------------------------------------------------------------
OS="$(uname -s 2>/dev/null || echo unknown)"
case "$OS" in
  Linux)
    command -v bwrap >/dev/null 2>&1 || die "no isolation available: bubblewrap (bwrap) is required on Linux to run repository code; refusing to run it unisolated" ;;
  Darwin)
    command -v sandbox-exec >/dev/null 2>&1 || die "no isolation available: sandbox-exec is required on macOS to run repository code; refusing to run it unisolated" ;;
  *) die "no isolation available on $OS; refusing to run repository code unisolated" ;;
esac
MECH=seatbelt
[ "$OS" = Linux ] && MECH=bwrap

# bwrap_base: the namespaces and the read-only system tree every sandbox starts from.
bwrap_base() {
  add --die-with-parent --new-session --unshare-all
  add --proc /proc --dev /dev --tmpfs /tmp
  for d in /bin /sbin /lib /lib32 /lib64 /libx32; do
    if [ -L "$d" ]; then add --symlink "$(readlink "$d")" "$d"
    elif [ -d "$d" ]; then add --ro-bind "$d" "$d"; fi
  done
  add --ro-bind /usr /usr
  for e in /etc/ssl /etc/ca-certificates /etc/pki /etc/alternatives /etc/ld.so.cache /etc/ld.so.conf /etc/ld.so.conf.d \
           /etc/localtime /etc/nsswitch.conf /etc/passwd /etc/group /etc/os-release /etc/mime.types /etc/java* /etc/dotnet; do
    [ -e "$e" ] && add --ro-bind "$e" "$e"
  done
  return 0
}

# self_test: build the cheapest possible sandbox and run a shell in it.
self_test() {
  if [ "$MECH" = bwrap ]; then
    ARGV=""
    bwrap_base
    eval "bwrap $ARGV /bin/sh -c :" >/dev/null 2>&1 \
      || die "bwrap cannot create a sandbox on this host (are unprivileged user namespaces disabled?); refusing to run repository code unisolated"
    ARGV=""
  else
    sandbox-exec -p '(version 1)(allow default)(deny network*)' /usr/bin/true >/dev/null 2>&1 \
      || die "sandbox-exec cannot create a sandbox on this host; refusing to run repository code unisolated"
  fi
}

if [ "$CHECK" = 1 ]; then self_test; exit 0; fi

[ $# -gt 0 ] || die "no command given"
[ -n "$RUN_DIR" ] || die "--run-dir is required"
[ -d "$RUN_DIR" ] || die "--run-dir is not a directory: $RUN_DIR"

# --- paths --------------------------------------------------------------------
# canon <path>: the physical path of an existing directory or file ("" if absent).
canon() {
  if [ -d "$1" ]; then (cd "$1" 2>/dev/null && pwd -P) || true
  elif [ -e "$1" ]; then
    d="$(cd "$(dirname "$1")" 2>/dev/null && pwd -P)" || return 0
    printf '%s/%s\n' "$d" "$(basename "$1")"
  fi
}

WS="$(canon "$RUN_DIR")"
[ -n "$WS" ] || die "cannot resolve --run-dir: $RUN_DIR"
PARENT="$(dirname "$WS")"
# The production layout is <workspace root>/instances/<instance id>: the root also
# holds every user's private and shared workflow files.
WSROOT=""
[ "$(basename "$PARENT")" = instances ] && WSROOT="$(dirname "$PARENT")"

mkdir -p "$WS/harden"
SCR="$(mktemp -d "$WS/harden/sandbox.XXXXXX")" || die "cannot create a scratch directory under $WS/harden"
SCR="$(canon "$SCR")"
# shellcheck disable=SC2317,SC2329 # invoked through trap (SC2317 on older shellcheck)
cleanup() { rm -rf "$SCR"; }
trap cleanup EXIT
trap 'cleanup; exit 143' HUP INT TERM

self_test

if [ -n "$CWD" ]; then
  CWD="$(canon "$CWD")"
  [ -d "$CWD" ] || die "--cwd does not exist"
else
  CWD="$WS"
fi

# Canonical read-write and read-only sets.
RW_C=""
for p in $(printf '%s' "$RW" | tr '\n' ' ') $(printf '%s' "${HARDEN_SANDBOX_RW:-}" | tr ':' ' '); do
  c="$(canon "$p")"
  [ -n "$c" ] || die "--rw path does not exist: $p"
  RW_C="$RW_C$c$NL"
done
RO_C=""
for p in $(printf '%s' "$RO" | tr '\n' ' ') $(printf '%s' "${HARDEN_SANDBOX_RO:-}" | tr ':' ' '); do
  c="$(canon "$p")"
  [ -n "$c" ] && RO_C="$RO_C$c$NL"
done

# A read-write path that is a git work tree keeps its git metadata read-only.
GIT_RO=""
for p in $(printf '%s' "$RW_C" | tr '\n' ' '); do
  [ -d "$p" ] || continue
  if [ -d "$p/.git" ]; then
    GIT_RO="$GIT_RO$p/.git$NL"
  elif [ -f "$p/.git" ]; then
    GIT_RO="$GIT_RO$p/.git$NL"
    gd="$(sed -n 's/^gitdir: //p' "$p/.git" | head -1)"
    case "$gd" in /*) ;; ?*) gd="$p/$gd" ;; esac
    if [ -n "$gd" ]; then
      gd="$(canon "$gd")"
      # <common>/worktrees/<name>: the common dir holds hooks and config.
      if [ -n "$gd" ] && [ "$(basename "$(dirname "$gd")")" = worktrees ]; then
        GIT_RO="$GIT_RO$(canon "$(dirname "$(dirname "$gd")")")$NL"
      elif [ -n "$gd" ]; then
        GIT_RO="$GIT_RO$gd$NL"
      fi
    fi
  fi
done

# --- environment ---------------------------------------------------------------
# PATH: the service's own, minus relative and missing entries. It is the
# operator's toolchain, so its directories are mounted (read-only) as they are.
SBX_PATH=""
PATH_DIRS=""
OLDIFS="$IFS"
IFS=:
for e in ${PATH:-/usr/bin:/bin}; do
  case "$e" in /*) ;; *) continue ;; esac
  c="$(canon "$e")"
  [ -n "$c" ] && [ -d "$c" ] || continue
  SBX_PATH="${SBX_PATH:+$SBX_PATH:}$e"
  PATH_DIRS="$PATH_DIRS$c$NL"
done
IFS="$OLDIFS"
[ -n "$SBX_PATH" ] || SBX_PATH="/usr/bin:/bin"
# The resolved Go toolchain comes first and is readable, never writable: one
# that is, or lies inside, the workspace, the scratch dir or a writable path is
# refused (a writable bind or allowance would win over the read-only grant).
if [ -n "$GO_ROOT" ]; then
  for _w in "$WS" "$SCR" $(printf '%s' "$RW_C" | tr '\n' ' '); do
    case "$GO_ROOT/" in "$_w"/*) die "the resolved Go toolchain $GO_ROOT is inside the writable path $_w; refusing to run it" ;; esac
    case "$_w/" in "$GO_ROOT"/*) die "the resolved Go toolchain $GO_ROOT contains the writable path $_w; refusing to run it" ;; esac
  done
  SBX_PATH="$GO_ROOT/bin:$SBX_PATH"
  RO_C="$RO_C$GO_ROOT$NL"
fi

HOME_S="$SCR/home"
mkdir -p "$HOME_S" "$SCR/tmp" "$SCR/cache" "$SCR/config" "$SCR/data"
# Go telemetry off (macOS and XDG config dirs): otherwise go starts a detached
# telemetry process that is still writing into the scratch HOME while it is
# removed ("Directory not empty").
for _t in "$HOME_S/Library/Application Support/go/telemetry" "$SCR/config/go/telemetry"; do
  mkdir -p "$_t" && printf 'off' > "$_t/mode"
done

# Dependency caches and toolchains that live under the host HOME, mounted
# read-only and linked into the scratch HOME at their usual relative paths. Only
# these exact subpaths: ~/.ssh, ~/.config, ~/.gitconfig, ~/.git-credentials,
# ~/.npmrc, ~/.m2/settings.xml, ~/.cargo/credentials.toml and the rest of HOME
# are not among them.
CACHE_SRC=""
REALHOME="${HOME:-}"
if [ -n "$REALHOME" ] && [ -d "$REALHOME" ]; then
  REALHOME="$(canon "$REALHOME")"
  for rel in go/pkg/mod go/bin .cargo/bin .cargo/registry .rustup .m2/repository .nuget/packages .dotnet \
             .local/share/pipx .local/pipx .local/bin .pyenv .volta .nvm .asdf .sdkman/candidates; do
    [ -e "$REALHOME/$rel" ] || continue
    mkdir -p "$HOME_S/$(dirname "$rel")"
    ln -s "$REALHOME/$rel" "$HOME_S/$rel" 2>/dev/null || true
    CACHE_SRC="$CACHE_SRC$REALHOME/$rel$NL"
  done
fi

NET_ALLOWED=0
[ "${ARBITRON_HARDEN_NETWORK:-}" = allow ] && NET_ALLOWED=1

# The command's whole environment. Nothing of the caller's is inherited.
ENV_PAIRS="$SCR/env.pairs"
{
  # .NET sockets are dual-mode IPv6, so testhost reaches vstest.console's
  # 127.0.0.1 listener as ::ffff:127.0.0.1, which Seatbelt's loopback rules do
  # not match: every `dotnet test` aborted. IPv4 sockets keep it on localhost.
  if [ "$MECH" = seatbelt ] && [ "$NET_ALLOWED" != 1 ]; then printf 'DOTNET_SYSTEM_NET_DISABLEIPV6=1\n'; fi
  printf 'TMPDIR=%s\n' "$SCR/tmp"
  printf 'XDG_CACHE_HOME=%s\n' "$SCR/cache"
  printf 'XDG_CONFIG_HOME=%s\n' "$SCR/config"
  printf 'XDG_DATA_HOME=%s\n' "$SCR/data"
  printf 'LANG=%s\n' "${LANG:-C.UTF-8}"
  printf 'TERM=dumb\nCI=1\nGIT_TERMINAL_PROMPT=0\nGIT_CONFIG_NOSYSTEM=1\nGIT_CONFIG_GLOBAL=/dev/null\n'
  printf 'GOPROXY=off\nGOSUMDB=off\nGOTOOLCHAIN=local\nGOCACHE=%s\n' "$SCR/cache/go-build"
  printf 'NPM_CONFIG_USERCONFIG=/dev/null\nNPM_CONFIG_CACHE=%s\n' "$SCR/cache/npm"
  printf 'DOTNET_CLI_HOME=%s\nDOTNET_SKIP_FIRST_TIME_EXPERIENCE=1\nDOTNET_CLI_TELEMETRY_OPTOUT=1\n' "$SCR/home"
  printf 'CARGO_HOME=%s\nCARGO_NET_OFFLINE=true\n' "$SCR/home/.cargo"
  for v in LC_ALL LC_CTYPE TZ JAVA_HOME DOTNET_ROOT GOROOT RUSTUP_HOME $(pass_names); do
    isset=""; val=""
    eval "isset=\${$v+x}"
    if [ "$isset" = x ]; then eval "val=\${$v}"; printf '%s=%s\n' "$v" "$val"; fi
  done
  [ -z "$GO_ROOT" ] || printf 'GOROOT=%s\n' "$GO_ROOT"
  printf '%s' "$ENVS"
} | sed '/^$/d' > "$ENV_PAIRS"

ENV_BIN="$(command -v env)"

# --- Linux: bubblewrap ----------------------------------------------------------
if [ "$MECH" = bwrap ]; then
  BWRAP="$(command -v bwrap)"
  add "$ENV_BIN" -i "PATH=$SBX_PATH" "HOME=$HOME_S"
  while IFS= read -r line; do add "$line"; done < "$ENV_PAIRS"
  add "$BWRAP"
  bwrap_base
  [ "$NET_ALLOWED" = 1 ] && add --share-net
  # Only the run's own workspace is mounted (readable); the --rw paths and the
  # scratch directory are writable inside it. Siblings and the workspace root are
  # simply not there.
  add --ro-bind "$WS" "$WS"
  for p in $(printf '%s%s%s' "$PATH_DIRS" "$RO_C" "$CACHE_SRC" | tr '\n' ' '); do
    case "$p" in "$WS"|"$WS"/*) continue ;; esac
    add --ro-bind "$p" "$p"
  done
  for p in $(printf '%s' "$RW_C" | tr '\n' ' '); do add --bind "$p" "$p"; done
  add --bind "$SCR" "$SCR"
  for p in $(printf '%s' "$GIT_RO" | tr '\n' ' '); do
    [ -e "$p" ] && add --ro-bind "$p" "$p"
  done
  add --chdir "$CWD" --
  add "$@"
else
# --- macOS: Seatbelt ----------------------------------------------------------------
  PROFILE="$SCR/profile.sb"
  {
    echo '(version 1)'
    echo '(allow default)'
    # Egress: loopback only (dotnet test talks to its test host over localhost).
    if [ "$NET_ALLOWED" != 1 ]; then
      echo '(deny network*)'
      echo '(allow network-outbound (remote ip "localhost:*"))'
      echo '(allow network-inbound (local ip "localhost:*"))'
      echo '(allow network-bind (local ip "localhost:*"))'
      echo "(allow network* (local unix-socket (subpath \"$SCR\")) (remote unix-socket (subpath \"$SCR\")))"
    fi
    # Credential services: no keychain, no security CLI.
    echo '(deny mach-lookup (global-name "com.apple.SecurityServer") (global-name "com.apple.securityd") (global-name "com.apple.secd") (global-name "com.apple.security.agent") (global-name "com.apple.security.syspolicy"))'
    echo '(deny process-exec (literal "/usr/bin/security"))'
    # Reads: the host's homes, other runs and the workspace root are off limits
    # (their contents and listings; stat stays allowed because realpath() of this
    # run's own paths walks the ancestors that sit above it).
    printf '(deny file-read-data'
    for d in /Users /home /root /private/var/root /Volumes /Library/Keychains /etc/arbitron /private/etc/arbitron \
             /var/lib/arbitron /private/var/lib/arbitron "$REALHOME" "$PARENT" "$WSROOT"; do
      [ -n "$d" ] && [ "$d" != / ] && printf ' (subpath "%s")' "$d"
    done
    echo ')'
    # ...except this run's own tree, the toolchain and the dependency caches.
    printf '(allow file-read-data (subpath "%s")' "$WS"
    for p in $(printf '%s%s%s' "$PATH_DIRS" "$RO_C" "$CACHE_SRC" | tr '\n' ' '); do printf ' (subpath "%s")' "$p"; done
    for p in $(printf '%s' "$RW_C" | tr '\n' ' '); do printf ' (subpath "%s")' "$p"; done
    echo ')'
    # Writes: the --rw paths, scratch and the null/tty devices, plus .NET's
    # named-mutex directory: on macOS the runtime keeps it at /tmp/.dotnet
    # whatever TMPDIR says, and the CLI's first-run NuGet migration (every run,
    # since the CLI home is scratch) takes such a mutex. Nothing else.
    echo '(deny file-write*)'
    printf '(allow file-write* (subpath "%s") (literal "/dev/null") (literal "/dev/dtracehelper") (regex #"^/dev/tty") (regex #"^/dev/fd/") (subpath "/private/tmp/.dotnet")' "$SCR"
    for p in $(printf '%s' "$RW_C" | tr '\n' ' '); do printf ' (subpath "%s")' "$p"; done
    echo ')'
    # Git metadata under a writable work tree stays read-only.
    for p in $(printf '%s' "$GIT_RO" | tr '\n' ' '); do
      [ -e "$p" ] && echo "(deny file-write* (subpath \"$p\"))"
    done
  } > "$PROFILE"
  add "$(command -v sandbox-exec)" -f "$PROFILE" "$ENV_BIN" -i "PATH=$SBX_PATH" "HOME=$HOME_S"
  while IFS= read -r line; do add "$line"; done < "$ENV_PAIRS"
  add "$@"
fi

eval "set -- $ARGV"
rc=0
(cd "$CWD" && exec "$@") || rc=$?
exit "$rc"
