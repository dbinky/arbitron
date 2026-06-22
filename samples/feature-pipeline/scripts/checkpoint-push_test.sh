#!/bin/sh
# Tests for checkpoint-push.sh — pushes the feature branch to origin.
#
# Runs the real script against a local bare origin + worktree (no network).
# Verifies:
#   A. It emits {pushed:true, branch:<branch>}.
#   B. The branch ref actually lands on origin.
#   C. It fails loudly (non-zero) when the push cannot succeed (no remote).
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/checkpoint-push.sh"

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

# --- Fixture: bare origin + a work clone on a feature branch with a commit ---
ORIGIN="$TMP/origin.git"
git init --quiet --bare "$ORIGIN"

WORK="$TMP/work"
git clone --quiet "$ORIGIN" "$WORK"
( cd "$WORK"
  git config user.email t@t && git config user.name t
  git checkout -q -b dev-thing-2026-01-01-abcd1234
  echo hi > f.txt && git add f.txt && git commit --quiet -m work )

# --- Case A/B: pushes the branch and reports it ---
out="$(printf '{"work_dir":"%s","branch":"%s"}' "$WORK" "dev-thing-2026-01-01-abcd1234" | sh "$SCRIPT" 2>/dev/null)"
pushed="$(printf '%s' "$out" | jq -r .pushed)"
branch="$(printf '%s' "$out" | jq -r .branch)"
check "emits pushed:true" "$pushed" "true"
check "echoes the branch" "$branch" "dev-thing-2026-01-01-abcd1234"
if git -C "$ORIGIN" rev-parse --verify --quiet "refs/heads/dev-thing-2026-01-01-abcd1234" >/dev/null; then
  echo "ok   - branch ref landed on origin"
else
  echo "FAIL - branch ref not present on origin"
  fails=$((fails + 1))
fi

# --- Case C: a worktree with no usable remote fails loudly ---
NOREMOTE="$TMP/noremote"
git init --quiet "$NOREMOTE"
( cd "$NOREMOTE"
  git config user.email t@t && git config user.name t
  git checkout -q -b dev-x
  echo hi > f.txt && git add f.txt && git commit --quiet -m x )
if printf '{"work_dir":"%s","branch":"%s"}' "$NOREMOTE" "dev-x" | sh "$SCRIPT" >/dev/null 2>&1; then
  echo "FAIL - push with no remote should have failed but succeeded"
  fails=$((fails + 1))
else
  echo "ok   - push failure surfaces as non-zero exit"
fi

echo "---"
if [ "$fails" -eq 0 ]; then
  echo "PASS"
else
  echo "$fails check(s) FAILED"
  exit 1
fi
