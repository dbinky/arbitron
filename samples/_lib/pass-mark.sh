#!/bin/sh
# Reads the variable snapshot as JSON on stdin; emits {sha, pass_id}.
# sha is work_dir's HEAD. pass_id is a monotonic nonce kept in
# <workspace>/tamper/pass-id (previous value + 1, 0+1 when absent) and written
# back on every run, so a loop pass is told apart even when HEAD did not move.
set -eu

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"

[ -n "$WORK" ] || { echo "pass-mark: work_dir not set" >&2; exit 1; }
[ -n "$WS" ] || { echo "pass-mark: workspace not set" >&2; exit 1; }

SHA="$(git -C "$WORK" rev-parse HEAD 2>/dev/null)" || { echo "pass-mark: not a git repository with a HEAD: $WORK" >&2; exit 1; }

FILE="$WS/tamper/pass-id"
PREV=0
if [ -f "$FILE" ]; then
  PREV="$(tr -d ' \n' < "$FILE")"
  case "$PREV" in
    ''|*[!0-9]*) echo "pass-mark: $FILE does not hold an integer" >&2; exit 1 ;;
  esac
fi
ID=$((PREV + 1))

mkdir -p "$WS/tamper"
printf '%s\n' "$ID" > "$FILE"

jq -n --arg sha "$SHA" --argjson id "$ID" '{sha:$sha, pass_id:$id}'
