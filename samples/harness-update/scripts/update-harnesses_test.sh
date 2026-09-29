#!/bin/sh
# Tests for update-harnesses.sh — the daily agent-harness updater.
#
# Every harness is a fake on a private PATH: it reports a version from a state
# file, and its updater bumps that version (or fails, on request). Nothing real
# is updated.
#
# Verifies:
#   A. Every installed harness is updated through its own updater, and the
#      result names the before/after versions.
#   B. One failing updater does not stop the others; the run exits non-zero so
#      the failure is visible, and still reports every harness.
#   C. A harness that is not installed is skipped, not failed.
#   D. An npm-installed harness (pi, opencode) that is running is deferred rather
#      than replaced in place under a live agent; claude and codex, which swap a
#      symlink, update regardless.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/update-harnesses.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { # desc actual expected
  if [ "$2" = "$3" ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1"
    echo "        expected: $3"
    echo "        actual:   $2"
    fails=$((fails + 1))
  fi
}

# fake <bin> <name> <version> <update-arg...>: a harness whose `--version` prints
# the state file and whose updater (called with the given args) bumps it to
# <version>.next, unless $STATE/<name>.fail exists.
fake() {
  bin="$1"; name="$2"; ver="$3"; shift 3
  printf '%s' "$ver" > "$STATE/$name.version"
  cat > "$bin/$name" <<SH
#!/bin/sh
if [ "\$1" = "--version" ]; then echo "$name \$(cat "$STATE/$name.version")"; exit 0; fi
if [ "\$*" = "$*" ]; then
  echo "\$*" >> "$STATE/$name.log"
  [ -f "$STATE/$name.fail" ] && { echo "network unreachable" >&2; exit 3; }
  printf '%s' "\$(cat "$STATE/$name.version").1" > "$STATE/$name.version"
  exit 0
fi
echo "unexpected args: \$*" >&2; exit 64
SH
  chmod +x "$bin/$name"
}

setup() { # dir -> sets BIN/STATE, all four harnesses installed, none running
  rm -rf "$1"; BIN="$1/bin"; STATE="$1/state"; mkdir -p "$BIN" "$STATE"
  ln -s "$(command -v jq)" "$BIN/jq"
  fake "$BIN" claude 2.1.0 update
  fake "$BIN" codex 0.159.0 update
  fake "$BIN" pi 0.99.1 update --self
  fake "$BIN" opencode 1.18.33 upgrade
  # pgrep stub: exits 0 (a match) only for patterns listed in $STATE/running.
  cat > "$BIN/pgrep" <<SH
#!/bin/sh
pat="\$2"
[ -f "$STATE/running" ] && grep -qxF -- "\$pat" "$STATE/running" && exit 0
exit 1
SH
  chmod +x "$BIN/pgrep"
}

run() { # -> stdout JSON; exit code in $RC
  RC=0
  OUT="$(printf '{"workspace":"/tmp/x"}' | PATH="$BIN:/usr/bin:/bin" sh "$SCRIPT" 2>/dev/null)" || RC=$?
}
status_of() { printf '%s' "$OUT" | jq -r --arg t "$1" '.results[] | select(.tool==$t) | .status'; }

# --- A: everything updates ---
setup "$TMP/a"; run
check "A: exits 0 when every update succeeds" "$RC" "0"
check "A: claude updated" "$(status_of claude)" "updated"
check "A: codex updated" "$(status_of codex)" "updated"
check "A: pi updated via 'pi update --self'" "$(cat "$STATE/pi.log")" "update --self"
check "A: opencode updated via 'opencode upgrade'" "$(cat "$STATE/opencode.log")" "upgrade"
check "A: before/after versions are reported" \
  "$(printf '%s' "$OUT" | jq -r '.results[] | select(.tool=="claude") | "\(.before)->\(.after)"')" "2.1.0->2.1.0.1"

# --- B: one failure does not stop the rest ---
setup "$TMP/b"; touch "$STATE/codex.fail"; run
check "B: a failed update exits non-zero" "$( [ "$RC" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
check "B: the failing harness is reported failed" "$(status_of codex)" "failed"
check "B: its error text is kept" \
  "$(printf '%s' "$OUT" | jq -r '.results[] | select(.tool=="codex") | .note' | grep -c 'network unreachable')" "1"
check "B: harnesses after it still update" "$(status_of opencode)" "updated"

# --- C: not installed is skipped, not failed ---
setup "$TMP/c"; rm "$BIN/pi"; run
check "C: a missing harness is skipped" "$(status_of pi)" "skipped"
check "C: ...and does not fail the run" "$RC" "0"

# --- D: running npm harnesses are deferred ---
setup "$TMP/d"; printf 'pi-coding-agent\nopencode\n' > "$STATE/running"; run
check "D: a running pi is deferred" "$(status_of pi)" "deferred"
check "D: ...and its updater is never called" "$( [ -f "$STATE/pi.log" ] && echo called || echo untouched)" "untouched"
check "D: a running opencode is deferred" "$(status_of opencode)" "deferred"
check "D: claude still updates (symlink swap is safe mid-run)" "$(status_of claude)" "updated"
check "D: deferral does not fail the run" "$RC" "0"

echo "---"
if [ "$fails" -eq 0 ]; then echo "PASS"; else echo "$fails check(s) FAILED"; exit 1; fi
