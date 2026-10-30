#!/bin/sh
# Tests for push-and-pr.sh — pushes the branch and opens the PR.
#
# WHY the blocked case (run 5e86d796, 2026-09-29): implementation stopped at 3 of
# 17 tasks with impl_blocked=true, the run continued into refinement as designed,
# and push-and-pr opened a normal, ready-for-review PR titled as the whole feature
# with no mention of the 14 blocked tasks. Only the run status said otherwise.
#
# Verifies (git and gh are fakes that log their arguments):
#   A. A complete run opens a ready-for-review PR (no --draft).
#   B. A blocked run opens a DRAFT PR whose title says it is blocked and whose body
#      carries the implementation assessor's account of what is blocked.
#   C. Both still push the branch and emit {pr_url, pr_number, branch}.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/push-and-pr.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { # desc actual expected
  if [ "$2" = "$3" ]; then echo "ok   - $1"
  else echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails + 1)); fi
}

setup() { # dir
  rm -rf "$1"; mkdir -p "$1/bin" "$1/work"; LOG="$1/log"; : > "$LOG"
  ln -s "$(command -v jq)" "$1/bin/jq"; ln -s "$(command -v sed)" "$1/bin/sed"
  cat > "$1/bin/git" <<SH
#!/bin/sh
printf 'git' >> "$LOG"; for a in "\$@"; do printf ' [%s]' "\$a" >> "$LOG"; done; echo >> "$LOG"
SH
  cat > "$1/bin/gh" <<SH
#!/bin/sh
printf 'gh' >> "$LOG"; for a in "\$@"; do printf ' [%s]' "\$a" >> "$LOG"; done; echo >> "$LOG"
[ "\$1" = "pr" ] && echo "https://github.com/acme/repo/pull/42"
exit 0
SH
  chmod +x "$1/bin/git" "$1/bin/gh"
}
run() { # dir json
  printf '%s' "$2" | PATH="$1/bin:/usr/bin:/bin" sh "$SCRIPT"
}
pr_line() { grep '^gh \[pr\] \[create\]' "$LOG"; }

# --- A: complete run ---
setup "$TMP/a"
OUT="$(run "$TMP/a" "{\"work_dir\":\"$TMP/a/work\",\"slug\":\"widgets\",\"branch\":\"dev-widgets-1\",\"base_branch\":\"main\"}")"
check "A: ready PR is not a draft" "$(pr_line | grep -c -- '\[--draft\]' || true)" "0"
check "A: title is the feature" "$(pr_line | grep -c -- '\[feat: widgets\]')" "1"
check "C: branch pushed" "$(grep -c '^git .*\[push\]' "$LOG")" "1"
check "C: emits pr_number" "$(printf '%s' "$OUT" | jq -r .pr_number)" "42"

# --- B: blocked run ---
setup "$TMP/b"
OUT="$(run "$TMP/b" "{\"work_dir\":\"$TMP/b/work\",\"slug\":\"widgets\",\"branch\":\"dev-widgets-1\",\"base_branch\":\"main\",\"impl_blocked\":true,\"impl-assess\":{\"tasks_remaining\":14,\"notes\":\"P3T4 and downstream tasks are blocked on four undecided proof contracts\"}}")"
check "B: blocked PR is a draft" "$(pr_line | grep -c -- '\[--draft\]')" "1"
check "B: title says blocked" "$(pr_line | grep -c -- '\[\[BLOCKED\] feat: widgets\]')" "1"
# The body spans several lines; only the `gh pr create` call carries one.
check "B: body carries the blockers" "$(grep -c 'four undecided proof contracts' "$LOG")" "1"
check "B: body says how many tasks remain" "$(grep -c '14 plan task(s) remain' "$LOG")" "1"
check "C: blocked run still emits pr_url" "$(printf '%s' "$OUT" | jq -r .pr_url)" "https://github.com/acme/repo/pull/42"

echo "---"
if [ "$fails" -eq 0 ]; then echo "PASS"; else echo "$fails check(s) FAILED"; exit 1; fi
