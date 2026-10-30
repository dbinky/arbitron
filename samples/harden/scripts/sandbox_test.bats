#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# sandbox.sh against the real enforcement mechanism of the host (Seatbelt on
# macOS, bubblewrap on Linux). A host with neither fails these tests: harden
# refuses to run repository code unisolated, so there is nothing to skip to.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/sandbox.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  # The production layout: <root>/instances/<id>, with the root holding users/ and shared/.
  ROOT="$T/root"
  WS="$ROOT/instances/inst1"
  SIB="$ROOT/instances/inst2"
  WORK="$WS/work"
  mkdir -p "$WORK" "$SIB" "$ROOT/users/u1" "$ROOT/shared" "$T/home/.ssh" "$T/home/.config/gh" "$T/outside"
  git init -q "$WORK"
  echo base > "$WORK/f"
  git -C "$WORK" add f
  git -C "$WORK" commit -q -m base
  echo "sibling-secret" > "$SIB/secret"
  echo "user-private" > "$ROOT/users/u1/private.md"
  echo "ssh-secret" > "$T/home/.ssh/id_ed25519"
  echo "gh-secret" > "$T/home/.config/gh/hosts.yml"
  echo "own-data" > "$WS/data"
  REAL_HOME="$HOME"
  export HOME="$T/home"
  DUMMY=dummy-token-77e1
  export GH_TOKEN="$DUMMY" GITHUB_TOKEN="$DUMMY" ANTHROPIC_API_KEY="$DUMMY" CLAUDE_CONFIG_DIR="$T/home/.claude" \
    SSH_AUTH_SOCK="$T/agent.sock" AWS_SECRET_ACCESS_KEY="$DUMMY"
  unset HARDEN_SANDBOX_RO HARDEN_SANDBOX_RW HARDEN_SANDBOX_PASS ARBITRON_HARDEN_NETWORK
}

sbx() { sh "$SCRIPT" --run-dir "$WS" "$@"; }

# Off-host targets (TEST-NET-1 and the IPv6 documentation prefix) never answer,
# so a timeout or an unreachable route would prove nothing: an open profile also
# fails to connect. The sandbox must refuse the connect itself, at once, with the
# mechanism's own error: EPERM from Seatbelt, ENETUNREACH from a network
# namespace that has only lo. (The host's own interface addresses are not a
# useful target: Seatbelt counts them as "localhost", like 127.0.0.1.)
denied_errno() { if [ "$(uname -s)" = Darwin ]; then echo 1; else echo 101; fi; }
# (musl says "Network unreachable", glibc "Network is unreachable".)
denied_re() { if [ "$(uname -s)" = Darwin ]; then echo "Operation not permitted"; else echo "Network (is )?unreachable"; fi; }

@test "--check passes where a sandbox can be built" {
  run --separate-stderr sh "$SCRIPT" --check
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
}

@test "the command sees none of the service's credentials or HOME" {
  run --separate-stderr sbx -- env
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" != *"$DUMMY"* ]]
  for name in GH_TOKEN GITHUB_TOKEN ANTHROPIC_API_KEY CLAUDE_CONFIG_DIR SSH_AUTH_SOCK AWS_SECRET_ACCESS_KEY; do
    [[ "$output" != *"$name="* ]] || { echo "$name leaked"; return 1; }
  done
  [[ "$output" == *"HOME=$WS/harden/sandbox."* || "$output" == *"HOME=$(cd "$WS" && pwd -P)/harden/sandbox."* ]]
  [[ "$output" == *"CI=1"* ]]
}

# Scrubbing the command's own environment is not enough when the command can read
# another process's: macOS exposes every same-user process's exec-time
# environment through sysctl(KERN_PROCARGS2), which Seatbelt does not mediate. The
# token is fresh per test (a concurrent suite run holds a different one) and is
# handed to the probe in two halves, so only a process environment can hold it.
procenv_token() { PE_A="procenv-probe-$$"; PE_B="$RANDOM$RANDOM"; export GH_TOKEN="$PE_A-$PE_B" GITHUB_TOKEN="$PE_A-$PE_B"; }

# Apple's own /bin/sh is a platform binary, whose environment KERN_PROCARGS2
# withholds; the leak needs sandbox.sh hosted by any other shell (Homebrew bash,
# dash), as an operator's PATH may well provide.
non_apple_sh() {
  for c in "$(command -v bash || true)" /opt/homebrew/bin/bash /usr/local/bin/bash /opt/homebrew/bin/dash /usr/local/bin/dash; do
    case "$c" in ''|/bin/*|/usr/bin/*) continue ;; esac
    [ -x "$c" ] && { printf '%s' "$c"; return 0; }
  done
  return 1
}

@test "macOS: a sandboxed child cannot read its parents' (or any process's) environment" {
  [ "$(uname -s)" = Darwin ] || skip "KERN_PROCARGS2 is the macOS interface; on Linux the pid namespace hides other processes (see the Linux test)"
  command -v python3 >/dev/null || skip "python3 is needed for the sysctl probe"
  HOST_SH="$(non_apple_sh)" || skip "no non-Apple shell to host sandbox.sh (Apple's /bin/sh withholds its own environment)"
  procenv_token
  cat > "$WS/probe.py" <<'PY'
import ctypes, os, sys
libc = ctypes.CDLL(None, use_errno=True)
needle = (sys.argv[1] + "-" + sys.argv[2]).encode()
def procargs(pid):
    mib = (ctypes.c_int * 3)(1, 49, pid)  # CTL_KERN, KERN_PROCARGS2
    n = ctypes.c_size_t(0)
    if libc.sysctl(mib, 3, None, ctypes.byref(n), None, 0) != 0:
        return None
    buf = ctypes.create_string_buffer(n.value)
    if libc.sysctl(mib, 3, buf, ctypes.byref(n), None, 0) != 0:
        return None
    return buf.raw[:n.value]
me, readable = os.getpid(), 0
parent = procargs(os.getppid())
print("parent-readable" if parent is not None else "parent-unreadable")
for pid in range(1, 100000):
    if pid == me:
        continue
    a = procargs(pid)
    if a is None:
        continue
    readable += 1
    if needle in a:
        print("LEAK", pid)
print("readable", readable)
PY
  for host in "$HOST_SH" sh; do
    run --separate-stderr "$host" "$SCRIPT" --run-dir "$WS" -- python3 "$WS/probe.py" "$PE_A" "$PE_B"
    [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
    [[ "$output" != *LEAK* ]] || { echo "under $host: $output"; return 1; }
    # Not vacuous: the probe does read the parent and other processes.
    [[ "$output" == *parent-readable* && "$output" == *"readable "[1-9]* ]] || { echo "$output"; return 1; }
  done
  # A variable a workflow node could set does not skip the scrub.
  HARDEN_SANDBOX_SCRUBBED=1 run --separate-stderr "$HOST_SH" "$SCRIPT" --run-dir "$WS" -- python3 "$WS/probe.py" "$PE_A" "$PE_B"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" != *LEAK* ]] || { echo "with the old marker preset: $output"; return 1; }
  # The command's own environment is clean too.
  run --separate-stderr "$HOST_SH" "$SCRIPT" --run-dir "$WS" -- env
  [[ "$output" != *"$PE_A-$PE_B"* ]]
}

@test "Linux: a sandboxed child sees no process environment holding the caller's token" {
  [ "$(uname -s)" = Linux ] || skip "/proc/<pid>/environ is the Linux interface; macOS is covered by the KERN_PROCARGS2 test"
  procenv_token
  run --separate-stderr sbx -- sh -c 'for e in /proc/[0-9]*/environ; do tr "\0" "\n" < "$e" 2>/dev/null | grep -q "$1-$2" && echo "LEAK $e"; done; echo done' _ "$PE_A" "$PE_B"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" != *LEAK* ]] || { echo "$output"; return 1; }
  [[ "$output" == *done* ]]
}

@test "the scrubbed relaunch keeps the operator's knobs and pass-through values" {
  export JAVA_HOME=/opt/jdk MY_TOOL_LOCATION=/opt/tool HARDEN_SANDBOX_PASS="MY_TOOL_LOCATION" HARDEN_SANDBOX_RW="$T/outside"
  run --separate-stderr sbx --cwd "$WS" -- sh -c 'echo ok > "$1/w" && printf "%s %s" "$JAVA_HOME" "$MY_TOOL_LOCATION"' _ "$T/outside"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$output" = "/opt/jdk /opt/tool" ]
  [ "$(cat "$T/outside/w")" = ok ]
}

@test "a forged relaunch sentinel with an unscrubbed environment is refused (125)" {
  run --separate-stderr sh "$SCRIPT" --sandbox-relaunched-scrubbed --run-dir "$WS" -- sh -c "echo ran > '$T/ran'"
  [ "$status" -eq 125 ]
  [[ "$stderr" == *"not scrubbed"* ]]
  [ ! -e "$T/ran" ]
}

@test "a relaunch that cannot work (script fed on stdin or via sh -c) exits 125, not 126/127" {
  run --separate-stderr sh -s -- --run-dir "$WS" -- sh -c "echo ran > '$T/ran'" < "$SCRIPT"
  [ "$status" -eq 125 ] || { echo "$status $stderr"; return 1; }
  [[ "$stderr" == *"cannot relaunch"* ]]
  run --separate-stderr sh -c "$(cat "$SCRIPT")" sandbox --run-dir "$WS" -- sh -c "echo ran > '$T/ran'"
  [ "$status" -eq 125 ] || { echo "$status $stderr"; return 1; }
  [ ! -e "$T/ran" ]
}

@test "pass-through names are never globbed and must be identifiers" {
  mkdir -p "$T/cwd" && touch "$T/cwd/GLOBME_FILE"
  export GLOBME_FILE=globbed OK_NAME=ok HARDEN_SANDBOX_PASS='1 GLOBME_* 9BAD OK_NAME'
  run --separate-stderr sh -c 'cd "$1" && shift && sh "$@"' _ "$T/cwd" "$SCRIPT" --run-dir "$WS" -- env
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" == *"OK_NAME=ok"* ]]
  [[ "$output" != *"GLOBME_FILE"* ]] || { echo "$output"; return 1; }
  [[ "$output" != *$'\n1='* && "$output" != *$'\n9BAD='* ]] || { echo "$output"; return 1; }
}

@test "operator pass-through names are the only way an extra variable gets in" {
  export JAVA_HOME=/opt/jdk MY_TOOL_LOCATION=/opt/tool
  run --separate-stderr sbx -- env
  [[ "$output" == *"JAVA_HOME=/opt/jdk"* ]]
  [[ "$output" != *"MY_TOOL_LOCATION"* ]]
  export HARDEN_SANDBOX_PASS="MY_TOOL_LOCATION"
  run --separate-stderr sbx -- env
  [[ "$output" == *"MY_TOOL_LOCATION=/opt/tool"* ]]
  [[ "$output" != *"$DUMMY"* ]]
}

@test "--env pairs are passed, with values that hold spaces and quotes" {
  run --separate-stderr sbx --env "A_B=x y 'z'" -- sh -c 'printf "%s" "$A_B"'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$output" = "x y 'z'" ]
}

@test "the run's own workspace is readable, and --rw paths are writable" {
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c 'cat ../data && echo changed > f && cat f'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$output" = "$(printf 'own-data\nchanged')" ]
  [ "$(cat "$WORK/f")" = changed ]
}

@test "nothing outside the --rw paths and scratch is writable" {
  # On macOS /tmp is the host's (only /tmp/.dotnet is open); on Linux the
  # sandbox has a private tmpfs /tmp, so writing there touches nothing.
  extra=()
  [ "$(uname -s)" = Darwin ] && extra=("/tmp/sandbox-test-planted-$$" "/tmp/.dotnetX-planted-$$")
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c '
    for t in "$@"; do
      if ( : > "$t" ) 2>/dev/null; then echo "WROTE $t"; else echo "denied $t"; fi
    done' _ "$SIB/planted" "$T/home/planted" "$T/outside/planted" "$ROOT/shared/planted" "$WS/data-planted" "${extra[@]}"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" != *WROTE* ]] || { echo "$output"; return 1; }
  [ ! -e "$SIB/planted" ] && [ ! -e "$T/home/planted" ] && [ ! -e "$T/outside/planted" ] && [ ! -e "$ROOT/shared/planted" ] && [ ! -e "$WS/data-planted" ]
}

@test "the host HOME, other users' files, the workspace root and sibling runs cannot be read" {
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c '
    for t in "$1" "$2" "$3" "$4" "$5"; do
      if cat "$t" >/dev/null 2>&1; then echo "READ $t"; else echo "denied $t"; fi
    done
    if ls "$6" 2>/dev/null | grep -q inst2; then echo "LISTED $6"; else echo "denied $6"; fi' \
    _ "$SIB/secret" "$T/home/.ssh/id_ed25519" "$T/home/.config/gh/hosts.yml" "$ROOT/users/u1/private.md" "$WS/data" "$ROOT/instances"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" != *READ*"/secret"* && "$output" != *READ*"id_ed25519"* && "$output" != *READ*"hosts.yml"* && "$output" != *READ*"private.md"* ]] || { echo "$output"; return 1; }
  [[ "$output" != *LISTED* ]] || { echo "$output"; return 1; }
  # ...while the run's own data stays readable.
  [[ "$output" == *"READ $WS/data"* ]]
}

@test "a git work tree's .git stays read-only so a hook or config cannot be planted" {
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c '
    for t in .git/hooks/pre-commit .git/config .git/HEAD; do
      if ( echo x >> "$t" ) 2>/dev/null; then echo "WROTE $t"; else echo "denied $t"; fi
    done
    echo new > untracked && echo "wrote-untracked"'
  [[ "$output" != *WROTE* ]] || { echo "$output"; return 1; }
  [[ "$output" == *wrote-untracked* ]]
  [ ! -e "$WORK/.git/hooks/pre-commit" ]
  [ -z "$(git -C "$WORK" config --local --get core.hooksPath || true)" ]
}

@test "a linked worktree's .git file and its common git dir are read-only too" {
  rm -rf "$WORK"
  SRC="$WS/src"
  git init -q "$SRC"
  echo base > "$SRC/f"; git -C "$SRC" add f; git -C "$SRC" commit -q -m base
  git -C "$SRC" worktree add -q --detach "$WORK" HEAD
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c '
    for t in .git "$1/hooks/commit-msg" "$1/config"; do
      if ( echo x >> "$t" ) 2>/dev/null; then echo "WROTE $t"; else echo "denied $t"; fi
    done' _ "$SRC/.git"
  [[ "$output" != *WROTE* ]] || { echo "$output"; return 1; }
  [ ! -e "$SRC/.git/hooks/commit-msg" ]
  grep -q '^gitdir: ' "$WORK/.git"
}

@test "the network is closed: no route off the host" {
  command -v bash >/dev/null || skip "bash is needed for /dev/tcp"
  run --separate-stderr sbx -- bash -c '( exec 3<>/dev/tcp/192.0.2.1/80 ) 2>&1; echo "rc=$?"'
  [[ "$output" == *"rc=1"* ]] || { echo "$output"; return 1; }
  # Refused by the sandbox, not timed out on an unroutable address.
  re="$(denied_re)"
  [[ "$output" =~ $re ]] || { echo "$output"; return 1; }
}

# vstest.console listens on 127.0.0.1 and testhost connects from a dual-mode
# IPv6 socket (::ffff:127.0.0.1), which Seatbelt's "localhost" does not match:
# without help, every C# test run aborted after the connection timeout.
@test "dotnet test (vstest.console <-> testhost over loopback) runs inside the sandbox" {
  command -v dotnet >/dev/null || skip "dotnet is not installed"
  P="$WORK/cs"
  # Nothing is written under the developer's real HOME: the CLI home, template
  # cache and package folder are the test's own, and restore reads the real NuGet
  # cache only as an offline feed.
  FEED="$REAL_HOME/.nuget/packages"
  [ -d "$FEED" ] || skip "no local NuGet cache to restore the xunit project from offline"
  export DOTNET_CLI_HOME="$T/dotnet-home" NUGET_PACKAGES="$T/nuget" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 \
    DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 MSBUILDDISABLENODEREUSE=1 UseSharedCompilation=false
  # Scaffold, restore and build outside the sandbox, as harden's clone step leaves
  # it. No build servers: they would outlive the test holding bats' fd 3.
  ( dotnet new xunit -o "$P" --no-restore && dotnet restore "$P" --source "$FEED" \
      && dotnet build "$P" --no-restore --disable-build-servers ) >/dev/null 2>&1 3>&- \
    || skip "could not scaffold, restore offline and build an xunit project (template or packages missing from the cache)"
  export VSTEST_CONNECTION_TIMEOUT=20 HARDEN_SANDBOX_PASS="VSTEST_CONNECTION_TIMEOUT NUGET_PACKAGES"
  run --separate-stderr sbx --rw "$WORK" --cwd "$P" -- dotnet test --no-build 3>&-
  [ "$status" -eq 0 ] || { echo "$output"; echo "$stderr"; return 1; }
  [[ "$output" == *"Passed!"* ]]
}

@test "the loopback allowance stays loopback: no outbound connection off the host" {
  command -v python3 >/dev/null || skip "python3 is needed for the socket probe"
  # Plain IPv4, a dual-mode IPv6 socket to the v4-mapped address (how .NET would
  # connect without DOTNET_SYSTEM_NET_DISABLEIPV6), and plain IPv6.
  run --separate-stderr sbx -- python3 -c '
import socket
for fam, host in ((socket.AF_INET, "192.0.2.1"), (socket.AF_INET6, "::ffff:192.0.2.1"), (socket.AF_INET6, "2001:db8::1")):
    s = socket.socket(fam, socket.SOCK_STREAM); s.settimeout(5)
    try:
        s.connect((host, 80)); print("CONNECTED", host)
    except OSError as e:
        print("errno", host, e.errno)
'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  e="$(denied_errno)"
  [ "$output" = "$(printf 'errno 192.0.2.1 %s\nerrno ::ffff:192.0.2.1 %s\nerrno 2001:db8::1 %s' "$e" "$e" "$e")" ] || { echo "$output"; return 1; }
}

# --- Go toolchain ---------------------------------------------------------------
# A host's base `go` may be older than a module's go directive and reach the
# right release only through GOTOOLCHAIN=auto, which the closed sandbox (local,
# no proxy, no sumdb) cannot do. sandbox.sh resolves the toolchain OUTSIDE, in
# the module, with the caller's own Go settings, and runs that GOROOT inside.
go_stubs() {
  # The stub GOROOT sits under the host HOME, which the sandbox cannot read: only
  # its read-only grant makes it reachable (on Seatbelt and bwrap alike).
  GOB="$T/gobase"; GR="$T/home/sdk/goroot-new"
  mkdir -p "$GOB" "$GR/bin" "$GR/src" "$T/gocalls"
  # The stub logs its calls from inside the sandbox too.
  export HARDEN_SANDBOX_RW="$T/gocalls"
  cat > "$GOB/go" <<EOF
#!/bin/sh
echo "\$(pwd) \$*" >> '$T/gocalls/base-go.calls'
case "\$*" in
  "env GOROOT") [ -n "\${STUB_GOROOT_FAIL:-}" ] && { echo "go: downloading go1.99 failed" >&2; exit 1; }; echo "\${STUB_GOROOT-$GR}" ;;
  version) echo "go version go1.20 base" ;;
esac
EOF
  cat > "$GR/bin/go" <<'EOF'
#!/bin/sh
echo "go version go1.99 resolved GOTOOLCHAIN=$GOTOOLCHAIN GOROOT=$GOROOT"
EOF
  chmod +x "$GOB/go" "$GR/bin/go"
  printf 'module toy\n\ngo 1.99\n' > "$WORK/go.mod"
  mkdir -p "$WORK/pkg"
  export PATH="$GOB:$PATH"
}

@test "go: a module newer than the base go runs the toolchain resolved outside" {
  go_stubs
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK/pkg" -- go version
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$output" = "go version go1.99 resolved GOTOOLCHAIN=local GOROOT=$(cd "$GR" && pwd -P)" ] || { echo "$output"; return 1; }
  # Resolved once, outside the sandbox, from inside the module.
  [ "$(cat "$T/gocalls/base-go.calls")" = "$WORK/pkg env GOROOT" ] || { cat "$T/gocalls/base-go.calls"; return 1; }
}

@test "go: the resolved GOROOT is read-only inside the sandbox" {
  go_stubs
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c 'cat "$1/bin/go" >/dev/null && echo readable; ( : > "$1/src/planted" ) 2>/dev/null && echo WROTE; true' _ "$GR"
  [[ "$output" == *readable* && "$output" != *WROTE* ]] || { echo "$output"; return 1; }
  [ ! -e "$GR/src/planted" ]
}

@test "go: a failed toolchain resolution refuses to run, saying why" {
  go_stubs
  export STUB_GOROOT_FAIL=1
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c "echo ran > '$T/ran'"
  [ "$status" -eq 125 ]
  [[ "$stderr" == *"cannot resolve the Go toolchain"* ]] || { echo "$stderr"; return 1; }
  [ ! -e "$T/ran" ]
}

@test "go: a resolved GOROOT without bin/go is refused" {
  go_stubs
  mkdir -p "$T/not-a-goroot"
  export STUB_GOROOT="$T/not-a-goroot"
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c "echo ran > '$T/ran'"
  [ "$status" -eq 125 ]
  [[ "$stderr" == *"not a Go toolchain"* ]] || { echo "$stderr"; return 1; }
  [ ! -e "$T/ran" ]
  # Boundary: an empty answer is refused too.
  export STUB_GOROOT=""
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- true
  [ "$status" -eq 125 ]
}

@test "go: outside a Go module nothing is resolved and PATH is the caller's" {
  go_stubs
  rm "$WORK/go.mod"
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c 'go version; echo "$PATH"'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" == "go version go1.20 base"* ]] || { echo "$output"; return 1; }
  [[ "$output" != *"$GR"* ]]
  [ "$(cat "$T/gocalls/base-go.calls")" = "$(cd "$WORK" && pwd -P) version" ]
}

# The one step that runs outside must never run the repository's own code: a
# relative or empty PATH entry (":$PATH", "bin:$PATH") must not turn `go` into
# a file the repository ships once the lookup happens inside the work tree.
plant_go() {
  mkdir -p "$(dirname "$1")"
  cat > "$1" <<EOF
#!/bin/sh
echo "PWNED outside: token=\$GH_TOKEN" > '$T/pwned'
echo /nonexistent
EOF
  chmod +x "$1"
}

@test "go: regression: a repository go reached through an empty PATH entry never runs" {
  go_stubs
  plant_go "$WORK/go"
  PATH=":$PATH" run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- go version
  [ ! -e "$T/pwned" ] || { cat "$T/pwned"; return 1; }
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" == "go version go1.99 resolved"* ]] || { echo "$output"; return 1; }
}

@test "go: regression: a repository go reached through a relative PATH entry never runs" {
  go_stubs
  plant_go "$WORK/bin/go"
  PATH="bin:$PATH" run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- true
  [ ! -e "$T/pwned" ] || { cat "$T/pwned"; return 1; }
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
}

@test "go: a go binary inside the workspace is refused, not run outside" {
  go_stubs
  plant_go "$WORK/tools/go"
  PATH="$WORK/tools:$PATH" run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c "echo ran > '$T/ran'"
  [ ! -e "$T/pwned" ] || { cat "$T/pwned"; return 1; }
  [ "$status" -eq 125 ]
  [[ "$stderr" == *"inside the run's workspace"* ]] || { echo "$stderr"; return 1; }
  [ ! -e "$T/ran" ]
}

@test "go: a relative GOROOT answer is refused" {
  go_stubs
  export STUB_GOROOT="goroot-rel"
  mkdir -p "$WORK/goroot-rel/bin" && cp "$GR/bin/go" "$WORK/goroot-rel/bin/go"
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c "echo ran > '$T/ran'"
  [ "$status" -eq 125 ]
  [[ "$stderr" == *"not an absolute"* ]] || { echo "$stderr"; return 1; }
  [ ! -e "$T/ran" ]
}

@test "go: a GOROOT inside the workspace or a writable path is refused" {
  go_stubs
  mkdir -p "$WS/fakeroot/bin" "$T/gocalls/fakeroot/bin"
  cp "$GR/bin/go" "$WS/fakeroot/bin/go"; cp "$GR/bin/go" "$T/gocalls/fakeroot/bin/go"
  for root in "$WS/fakeroot" "$T/gocalls/fakeroot"; do
    STUB_GOROOT="$root" run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c "echo ran > '$T/ran'"
    [ "$status" -eq 125 ] || { echo "$root: $status $output $stderr"; return 1; }
    [[ "$stderr" == *"writable"* ]] || { echo "$stderr"; return 1; }
  done
  [ ! -e "$T/ran" ]
}

@test "go: a go.work-only workspace counts as a Go module" {
  go_stubs
  rm "$WORK/go.mod"
  printf 'go 1.99\n' > "$WORK/go.work"
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- go version
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [[ "$output" == "go version go1.99 resolved"* ]] || { echo "$output"; return 1; }
}

@test "go: a forged relaunch with a clean environment cannot smuggle in a GOROOT" {
  go_stubs
  for bogus in /nonexistent "$WS/fakeroot" "relative/root" "$(printf '%s\n/x' "$GR")" "$GR:/tmp"; do
    run --separate-stderr env -i PATH="$PATH" HOME="$HOME" sh "$SCRIPT" --sandbox-relaunched-scrubbed "$bogus" --run-dir "$WS" -- sh -c "echo ran > '$T/ran'"
    [ "$status" -eq 125 ] || { echo "$bogus: $status $stderr"; return 1; }
  done
  [ ! -e "$T/ran" ]
}

@test "go: telemetry is off and the scratch directory is always removed" {
  command -v go >/dev/null || skip "go is not installed"
  printf 'module toy\n\ngo 1.21\n' > "$WORK/go.mod"
  run --separate-stderr sbx --rw "$WORK" --cwd "$WORK" -- sh -c '
    for f in "$HOME/Library/Application Support/go/telemetry/mode" "$XDG_CONFIG_HOME/go/telemetry/mode"; do cat "$f"; echo; done
    go env GOTELEMETRY; go version >/dev/null'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(printf '%s\n' "$output" | head -3)" = "$(printf 'off\noff\noff')" ] || { echo "$output"; return 1; }
  [ -z "$(ls "$WS/harden" 2>/dev/null | grep '^sandbox\.' || true)" ]
  [[ "$stderr" != *"Directory not empty"* ]] || { echo "$stderr"; return 1; }
}

@test "the scratch directory is removed afterwards, whatever the command did" {
  run --separate-stderr sbx -- sh -c 'echo junk > "$TMPDIR/junk"; exit 7'
  [ "$status" -eq 7 ]
  [ -z "$(ls "$WS/harden" 2>/dev/null | grep '^sandbox\.' || true)" ]
}

@test "the command's exit status is passed through" {
  run --separate-stderr sbx -- sh -c 'exit 3'
  [ "$status" -eq 3 ]
}

# --- Fail closed ---------------------------------------------------------------

@test "without bwrap or sandbox-exec nothing runs and the status is 125" {
  mkdir -p "$T/bin"
  for u in sh uname env dirname basename mkdir rm mktemp sed tr cat ln readlink; do
    p="$(command -v "$u")" && ln -sf "$p" "$T/bin/$u"
  done
  run --separate-stderr env PATH="$T/bin" sh "$SCRIPT" --run-dir "$WS" -- sh -c "echo ran > '$T/ran'"
  [ "$status" -eq 125 ]
  [[ "$stderr" == *"refusing to run"* ]]
  [ ! -e "$T/ran" ]
}

@test "--check also fails closed" {
  mkdir -p "$T/bin"
  for u in sh uname; do p="$(command -v "$u")" && ln -sf "$p" "$T/bin/$u"; done
  run --separate-stderr env PATH="$T/bin" sh "$SCRIPT" --check
  [ "$status" -eq 125 ]
}

@test "a missing --run-dir, command or workspace is refused before anything runs" {
  run --separate-stderr sh "$SCRIPT" -- true
  [ "$status" -eq 125 ]
  run --separate-stderr sh "$SCRIPT" --run-dir "$WS"
  [ "$status" -eq 125 ]
  run --separate-stderr sh "$SCRIPT" --run-dir "$T/absent" -- true
  [ "$status" -eq 125 ]
}
