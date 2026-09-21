#!/bin/sh
# Covers push-and-pr.sh's reporting of the COUNTED review-plan state.
#
# WHY THIS EXISTS: this script runs exactly once, at the end of a run that may
# have taken days, and its output is the only artifact most people ever read.
# Run a1aab074 exited on the stagnation backstop with 98 approval boxes unticked
# and opened a PR that said nothing about it — the shortfall was visible only to
# someone who went and read the instance trace. The gate fix stops that exit from
# happening for the wrong reason; this makes the right ones legible.
#
# It is also the branch most likely to rot unnoticed: a bad jq path here does not
# fail a test suite, it fails the last node of a 40-hour run, after the refinement
# work is done but before it is pushed. That has happened in this repo before.
#
# `gh` and the network are stubbed; git is real.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SUT="$SCRIPT_DIR/push-and-pr.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }
contains() { if printf '%s' "$2" | grep -q "$3"; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected to contain: $3"; echo "        actual: $2"; fails=$((fails+1)); fi; }
lacks() { if printf '%s' "$2" | grep -q "$3"; then
  echo "FAIL - $1"; echo "        expected NOT to contain: $3"; fails=$((fails+1)); else echo "ok   - $1"; fi; }

# --- stub gh: records the argv it was called with, prints a PR URL ------------
mkdir -p "$TMP/bin"
cat > "$TMP/bin/gh" <<'STUB'
#!/bin/sh
[ "$1" = "auth" ] && exit 0
printf '%s\n' "$@" > "$GH_ARGV_FILE"
echo "https://github.com/o/r/pull/999"
STUB
chmod +x "$TMP/bin/gh"
PATH="$TMP/bin:$PATH"; export PATH

# --- a real work repo with an origin it can push to --------------------------
WORK="$TMP/work"; ORIGIN="$TMP/origin.git"
git init --quiet --bare "$ORIGIN"
git init --quiet -b feature "$WORK"
git -C "$WORK" config user.email t@t; git -C "$WORK" config user.name t
echo hi > "$WORK/f"; git -C "$WORK" add f; git -C "$WORK" commit --quiet -m seed
git -C "$WORK" remote add origin "$ORIGIN"

run() { # stdin-json -> stdout
  GH_ARGV_FILE="$TMP/argv"; export GH_ARGV_FILE
  : > "$TMP/argv"
  printf '%s' "$1" | sh "$SUT" 2>/dev/null
}
body() { tr '\n' '\v' < "$TMP/argv" | sed 's/.*--body\v//' | tr '\v' '\n'; }

vars() { # unchecked_boxes summary base_branch
  jq -n --arg w "$WORK" --arg ub "$1" --arg s "$2" --arg bb "$3" \
    '{work_dir:$w, slug:"demo", branch:"feature", base_branch:$bb,
      baseline_summary:"3 pre-existing failing test(s)",
      "count-review-plan": ({summary:$s} + (if $ub == "" then {} else {unchecked_boxes:($ub|tonumber)} end))}'
}

# --- 1. incomplete plan: the PR must SAY it is incomplete ---------------------
OUT="$(run "$(vars 98 'Review plan INCOMPLETE: 98 approval box(es) still unticked.' main)")"
B="$(body)"
contains "incomplete: PR body flags the shortfall"   "$B" "INCOMPLETE REVIEW"
contains "incomplete: PR body names the box count"   "$B" "98 approval box"
contains "incomplete: PR body carries the counter's summary" "$B" "still unticked"
contains "incomplete: PR body still carries the baseline note" "$B" "Test baseline:"
check    "incomplete: PR url emitted" "$(printf '%s' "$OUT" | jq -r .pr_url)" "https://github.com/o/r/pull/999"

# --- 2. complete plan: reported, but not as a warning ------------------------
OUT="$(run "$(vars 0 'Review plan COMPLETE on the boxes: all 52 single-system and 92 paired reviews carry their required approvals.' main)")"
B="$(body)"
lacks    "complete: no false alarm"                  "$B" "INCOMPLETE REVIEW"
contains "complete: plan state still reported"       "$B" "Review plan: Review plan COMPLETE"
check    "complete: PR url emitted" "$(printf '%s' "$OUT" | jq -r .pr_url)" "https://github.com/o/r/pull/999"

# --- 3. older run with no counter output: degrade, never crash ---------------
OUT="$(run "$(jq -n --arg w "$WORK" '{work_dir:$w, slug:"demo", branch:"feature", base_branch:"main"}')")"
B="$(body)"
lacks    "no counter: no INCOMPLETE claim"           "$B" "INCOMPLETE REVIEW"
lacks    "no counter: no empty plan line"            "$B" "Review plan:"
check    "no counter: still opens the PR" "$(printf '%s' "$OUT" | jq -r .pr_url)" "https://github.com/o/r/pull/999"

# --- 4. resumed run (head == base): no PR, but the shortfall still reported --
OUT="$(run "$(vars 98 'Review plan INCOMPLETE: 98 approval box(es) still unticked.' feature)")"
check    "resumed: no PR created"  "$(printf '%s' "$OUT" | jq -r .pr_created)" "false"
check    "resumed: work is pushed" "$(printf '%s' "$OUT" | jq -r .pushed)"     "true"
contains "resumed: shortfall reported in the note" "$(printf '%s' "$OUT" | jq -r .note)" "INCOMPLETE REVIEW: 98 approval box"

# --- 5. resumed run on a complete plan: note stays clean --------------------
OUT="$(run "$(vars 0 'Review plan COMPLETE on the boxes.' feature)")"
lacks    "resumed+complete: no false alarm" "$(printf '%s' "$OUT" | jq -r .note)" "INCOMPLETE"
check    "resumed+complete: work is pushed" "$(printf '%s' "$OUT" | jq -r .pushed)" "true"

if [ "$fails" -eq 0 ]; then echo "all push-and-pr tests passed"; else
  echo "$fails push-and-pr test(s) FAILED"; exit 1; fi
