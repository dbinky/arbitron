#!/bin/sh
# Checks that the agent CLIs a stage is about to use are logged in, so an
# expired login stops the pipeline with a named reason instead of surfacing as
# an opaque "harness process failed" that is retried for hours (run 68225980).
#
#   auth-preflight.sh <cli>...
#
# <cli> is one of:
#   claude          `claude auth status --json` (ANTHROPIC_API_KEY unset, so the
#                   OAuth login is what is checked): loggedIn, and loginExpiresAt
#                   not in the past
#   codex           `codex login status` exits 0
#   pi:<provider>   `pi auth check --provider <provider>` exits 0 (ready)
#   pi              skipped with a note: pi has no provider-free status check
#
# Emits {ok, failures:[{cli, detail}], skipped:[{cli, detail}], checked:[cli],
# summary} and exits 0 whatever it finds: the caller routes on .ok. A CLI that
# is not on PATH is skipped (a stage that needs it fails on its own, with a
# clear error); an unknown name fails closed. No probe output is ever echoed,
# so no account detail or credential can reach the node output.
#
# Each probe gets ARBITRON_AUTH_PREFLIGHT_TIMEOUT seconds (default 20); a probe
# that runs over is a failure. ARBITRON_AUTH_PREFLIGHT=0 skips every probe
# (scripts see only allowlisted env, so set it through script.envpassthrough).
# Stdin is not read. Usage error (no <cli>): exit 2.
set -u

[ $# -gt 0 ] || { echo "usage: auth-preflight.sh <claude|codex|pi[:<provider>]>..." >&2; exit 2; }

TO="${ARBITRON_AUTH_PREFLIGHT_TIMEOUT:-20}"
case "$TO" in ''|*[!0-9]*) TO=20 ;; esac
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t authpf)"
trap 'rm -rf "$TMP"' EXIT
FAIL="$TMP/failures.ndjson"
SKIP="$TMP/skipped.ndjson"
: > "$FAIL"
: > "$SKIP"

fail() { jq -cn --arg c "$1" --arg d "$2" '{cli:$c, detail:$d}' >> "$FAIL"; }
skip() { jq -cn --arg c "$1" --arg d "$2" '{cli:$c, detail:$d}' >> "$SKIP"; }

# probe <outfile> <cmd>...: runs cmd with stdout to <outfile> (stderr dropped),
# killed after $TO seconds. Sets RC (124 on timeout).
probe() {
  _out="$1"; shift
  _mark="$TMP/timed-out.$$"
  rm -f "$_mark"
  "$@" > "$_out" 2>/dev/null < /dev/null &
  _pid=$!
  ( sleep "$TO"; : > "$_mark"; kill "$_pid" 2>/dev/null; sleep 1; kill -9 "$_pid" 2>/dev/null ) \
    > /dev/null 2>&1 < /dev/null &
  _wd=$!
  RC=0
  wait "$_pid" || RC=$?
  { kill "$_wd"; wait "$_wd"; } > /dev/null 2>&1 || true
  [ ! -e "$_mark" ] || RC=124
}

check_claude() {
  probe "$TMP/claude.json" env -u ANTHROPIC_API_KEY claude auth status --json
  if [ "$RC" -eq 124 ]; then fail claude "claude auth status timed out after ${TO}s"; return; fi
  verdict="$(jq -r '
    if (.loggedIn | type) != "boolean" then "unreadable"
    elif .loggedIn != true then "logged_out"
    elif (.loginExpiresAt // null) == null then "ok"
    elif ((.loginExpiresAt | tostring | sub("\\.[0-9]+Z$"; "Z") | try fromdateiso8601 catch null) // null) == null then "ok"
    elif (.loginExpiresAt | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) <= now then "expired"
    else "ok" end' "$TMP/claude.json" 2>/dev/null || echo unreadable)"
  case "$verdict" in
    ok) [ "$RC" -eq 0 ] || fail claude "claude auth status exited $RC although it reports a login" ;;
    logged_out) fail claude "claude is not logged in (claude auth status: loggedIn false); run 'claude auth login' as the server user" ;;
    expired) fail claude "claude login has expired (loginExpiresAt is in the past); run 'claude auth login' as the server user" ;;
    *) fail claude "claude auth status gave no readable login state (exit $RC)" ;;
  esac
}

check_codex() {
  probe "$TMP/codex.txt" codex login status
  if [ "$RC" -eq 124 ]; then fail codex "codex login status timed out after ${TO}s"; return; fi
  [ "$RC" -eq 0 ] || fail codex "codex is not logged in (codex login status exited $RC); run 'codex login' as the server user"
}

check_pi() { # <provider>
  probe "$TMP/pi.txt" pi auth check --provider "$1"
  if [ "$RC" -eq 124 ]; then fail "pi:$1" "pi auth check --provider $1 timed out after ${TO}s"; return; fi
  if [ "$RC" -ne 0 ]; then
    # The check prints one word (ready, not_ready, invalid); pass only that on.
    state="$(head -n 1 "$TMP/pi.txt" | tr -cd 'a-z_' | cut -c1-20)"
    fail "pi:$1" "pi provider $1 is ${state:-not ready} (pi auth check exited $RC); run 'pi auth login $1' as the server user"
  fi
}

for spec in "$@"; do
  if [ "${ARBITRON_AUTH_PREFLIGHT:-1}" = 0 ]; then
    skip "$spec" "auth preflight disabled (ARBITRON_AUTH_PREFLIGHT=0)"
    continue
  fi
  bin="${spec%%:*}"
  case "$spec" in
    claude|codex|pi|pi:?*) ;;
    *) fail "$spec" "unknown CLI '$spec' (want claude, codex, pi or pi:<provider>)"; continue ;;
  esac
  if ! command -v "$bin" > /dev/null 2>&1; then
    skip "$spec" "$bin is not on PATH; not checked"
    continue
  fi
  case "$spec" in
    claude) check_claude ;;
    codex) check_codex ;;
    pi) skip pi "pi has no provider-free status check; name the provider as pi:<provider> to check it" ;;
    pi:*) check_pi "${spec#pi:}" ;;
  esac
done

jq -n --slurpfile f "$FAIL" --slurpfile s "$SKIP" --args '
  {ok: ($f | length == 0), failures: $f, skipped: $s, checked: $ARGS.positional,
   summary: (if ($f | length) == 0 then "agent CLIs logged in"
             else "agent CLI auth preflight failed: " + ([$f[] | "\(.cli): \(.detail)"] | join("; ")) end)}' "$@"
