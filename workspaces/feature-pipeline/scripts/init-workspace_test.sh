#!/bin/sh
# Tests for init-workspace.sh — branch isolation and slug handling.
#
# Runs the real script against a local fixture repo (git clone/worktree work on
# local paths, no network). Verifies:
#   A. The branch name carries the per-instance id suffix, so two instances with
#      the same slug + run_date never collide on the shared remote.
#   B. An empty slug does NOT silently fall back to the repo name; it uses a
#      neutral placeholder instead.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/init-workspace.sh"
TODAY="$(date +%Y-%m-%d)"

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
check_contains() { # desc haystack needle present(=1)/absent(=0)
  case "$2" in
    *"$3"*) got=1 ;;
    *) got=0 ;;
  esac
  if [ "$got" = "$4" ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1 (substring '$3' present=$got, wanted=$4)"
    echo "        string: $2"
    fails=$((fails + 1))
  fi
}

# --- Fixture origin repo named "myrepo" (so basename fallback would be 'myrepo') ---
ORIGIN="$TMP/myrepo"
git -c init.defaultBranch=main init --quiet "$ORIGIN"
( cd "$ORIGIN"
  git config user.email t@t && git config user.name t
  echo hi > README.md && git add README.md && git commit --quiet -m init )

# Build a per-instance workspace dir whose basename is the instance id (matches
# the engine's instances/<uuid>/ layout), then run the script with given slug.
# repo_url uses file:// so `git clone --depth` is honored (git ignores --depth
# for plain local-path clones, but a real workflow clones an https/ssh URL).
run_case() { # instance_id slug  -> echoes emitted JSON
  iid="$1"; slug="$2"
  ws="$TMP/instances/$iid"
  mkdir -p "$ws"
  printf '{"workspace":"%s","repo_url":"file://%s","slug":"%s"}' "$ws" "$ORIGIN" "$slug" \
    | sh "$SCRIPT" 2>/dev/null
}

# --- Case B: empty slug -> neutral placeholder, never the repo name ---
out="$(run_case "11111111-2222-3333-4444-555555555555" "")"
branch="$(printf '%s' "$out" | jq -r .branch)"
slug_out="$(printf '%s' "$out" | jq -r .slug)"
check_contains "empty slug branch does not contain repo name" "$branch" "myrepo" 0
check "empty slug yields neutral placeholder slug" "$slug_out" "feature"
check "empty slug branch is dev-feature-<date>-<short8>" "$branch" "dev-feature-$TODAY-11111111"

# --- Case A: provided slug + per-instance suffix ---
out="$(run_case "aaaaaaaa-2222-3333-4444-555555555555" "cool-thing")"
branch="$(printf '%s' "$out" | jq -r .branch)"
slug_out="$(printf '%s' "$out" | jq -r .slug)"
check "provided slug is preserved" "$slug_out" "cool-thing"
check "branch carries slug + date + instance suffix" "$branch" "dev-cool-thing-$TODAY-aaaaaaaa"

# --- Case A (collision guard): same slug+date, different instances -> different branches ---
b1="$(run_case "dddddddd-0000-0000-0000-000000000000" "same" | jq -r .branch)"
b2="$(run_case "eeeeeeee-0000-0000-0000-000000000000" "same" | jq -r .branch)"
if [ "$b1" != "$b2" ]; then
  echo "ok   - two instances, same slug+date -> distinct branches"
else
  echo "FAIL - branch collision: both instances produced '$b1'"
  fails=$((fails + 1))
fi

# --- Shallow clone: each instance clones only the tip, not full history ---
ws="$TMP/instances/ffffffff-0000-0000-0000-000000000000"
mkdir -p "$ws"
printf '{"workspace":"%s","repo_url":"file://%s","slug":"x"}' "$ws" "$ORIGIN" | sh "$SCRIPT" 2>/dev/null >/dev/null
shallow="$(git -C "$ws/src" rev-parse --is-shallow-repository)"
check "clone is shallow (depth 1)" "$shallow" "true"

echo "---"
if [ "$fails" -eq 0 ]; then
  echo "PASS"
else
  echo "$fails check(s) FAILED"
  exit 1
fi
