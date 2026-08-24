#!/bin/sh
# Tests for record-pass-commit.sh — the refinement loop's progress signal.
#
# Case B is a regression test for the bug that hid a production livelock
# (instance b61b4f48). progress_evidence was simply "did this pass commit?", but
# the approver commits its verdict even when it WITHHOLDS approval. Every pass
# therefore looked productive, no_progress_streak stayed pinned at 0, and both
# the stagnation backstop and the Opus rescue became unreachable. The run made
# zero code changes and ticked zero of 246 approval boxes for 14 straight passes
# while the loop believed it was advancing.
#
# Verifies:
#   A. A commit touching real code counts as progress.
#   B. A commit touching ONLY the tracking files, ticking no box and closing no
#      gap, is scored as churn — this is what lets the loop escalate.
#   C. Ticking an approval box counts as progress even with no code change.
#   D. Closing a gap item counts as progress.
#   E. No new commit at all is churn.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/record-pass-commit.sh"

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

WS="$TMP/ws"
W="$WS/work"
mkdir -p "$W/docs/reference"
git -C "$W" init -q .
git -C "$W" config user.email test@example.com
git -C "$W" config user.name test
printf '| 1 | area | [ ] | [ ] |\n' > "$W/docs/reference/focus-areas.md"
printf -- '- [ ] gap one\n'          > "$W/docs/reference/gaps-identified.md"
echo "package main" > "$W/app.go"
git -C "$W" add -A
git -C "$W" commit -qm "chore: seed ralph review plan"

run() { printf '{"workspace":"%s","work_dir":"%s"}' "$WS" "$W" | sh "$SCRIPT"; }

# Prime the state files so later passes measure a true delta.
run > /dev/null

# --- Case A: real code change ---
echo "// more" >> "$W/app.go"
git -C "$W" add -A && git -C "$W" commit -qm "refine: a real code fix"
OUT="$(run)"
check "A: a code change counts as progress" \
  "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "true"
check "A: code_changed is reported" \
  "$(printf '%s' "$OUT" | jq -r '.code_changed')" "true"

# --- Case B: the livelock shape — approver withholds, touching only trackers ---
printf -- '- [ ] gap two\n' >> "$W/docs/reference/gaps-identified.md"
git -C "$W" add -A && git -C "$W" commit -qm "approve: withheld -- nothing to judge"
OUT="$(run)"
check "B: a tracker-only withheld commit is CHURN, not progress" \
  "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "false"
check "B: ...and adding a new unchecked gap does not count as a closed gap" \
  "$(printf '%s' "$OUT" | jq -r '.gaps_closed')" "0"

# --- Case C: an approval box actually gets ticked ---
sed 's/\[ \] | \[ \]/[x] | [ ]/' "$W/docs/reference/focus-areas.md" > "$W/f.tmp"
mv "$W/f.tmp" "$W/docs/reference/focus-areas.md"
git -C "$W" add -A && git -C "$W" commit -qm "approve: ticked area 1 review 1"
OUT="$(run)"
check "C: ticking an approval box is progress" \
  "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "true"
check "C: the tick is counted" \
  "$(printf '%s' "$OUT" | jq -r '.boxes_ticked')" "1"

# --- Case D: a gap item gets closed ---
sed 's/- \[ \] gap one/- [x] gap one/' "$W/docs/reference/gaps-identified.md" > "$W/g.tmp"
mv "$W/g.tmp" "$W/docs/reference/gaps-identified.md"
git -C "$W" add -A && git -C "$W" commit -qm "refine: close gap one"
OUT="$(run)"
check "D: closing a gap is progress" \
  "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "true"
check "D: the closure is counted" \
  "$(printf '%s' "$OUT" | jq -r '.gaps_closed')" "1"

# --- Case E: nothing committed at all ---
OUT="$(run)"
check "E: no new commit is churn" \
  "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "false"
check "E: commit count is zero" \
  "$(printf '%s' "$OUT" | jq -r '.new_commit_count')" "0"

# --- Case F: the approval-stall axis (deep-ralph-loop bea09188) --------------
# 15 consecutive passes wrote real production code against a receding target —
# each fix uncovered the adjacent variant — so code_changed was true every pass,
# the streak stayed at 0 and the escalation never fired. Busy is not converging.
STALL="$TMP/stall"
SW="$STALL/work"
mkdir -p "$SW/docs/reference"
git -C "$SW" init -q .
git -C "$SW" config user.email test@example.com
git -C "$SW" config user.name test
printf '| 1 | area | [ ] | [ ] |\n' > "$SW/docs/reference/focus-areas.md"
printf -- '- [ ] gap one\n'          > "$SW/docs/reference/gaps-identified.md"
echo "package main" > "$SW/app.go"
git -C "$SW" add -A && git -C "$SW" commit -qm "chore: seed ralph review plan"
srun() { printf '{"workspace":"%s","work_dir":"%s"}' "$STALL" "$SW" | sh "$SCRIPT"; }
srun > /dev/null

i=0
while [ "$i" -lt 6 ]; do
  i=$((i + 1))
  echo "// fix variant $i" >> "$SW/app.go"
  git -C "$SW" add -A && git -C "$SW" commit -qm "refine: fix variant $i"
  OUT="$(srun)"
done
check "F: 7 code-committing passes without an approval are still progress (priming pass counts as one)"   "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "true"
check "F: ...and the counter is tracking them"   "$(printf '%s' "$OUT" | jq -r '.passes_since_approval')" "7"

echo "// fix variant 8" >> "$SW/app.go"
git -C "$SW" add -A && git -C "$SW" commit -qm "refine: fix variant 8"
OUT="$(srun)"
check "F: the 8th approval-less pass trips the stall, despite real code"   "$(printf '%s' "$OUT" | jq -r '.approval_stalled')" "true"
check "F: ...and is scored as churn so the backstop can escalate"   "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "false"

echo "// fix variant 9" >> "$SW/app.go"
git -C "$SW" add -A && git -C "$SW" commit -qm "refine: fix variant 9"
OUT="$(srun)"
check "F: the stall is STICKY, so a second consecutive churn reaches the streak"   "$(printf '%s' "$OUT" | jq -r '.progress_evidence')" "false"

sed 's/\[ \] | \[ \]/[x] | [ ]/' "$SW/docs/reference/focus-areas.md" > "$SW/f.tmp"
mv "$SW/f.tmp" "$SW/docs/reference/focus-areas.md"
git -C "$SW" add -A && git -C "$SW" commit -qm "approve: ticked area 1 review 1"
OUT="$(srun)"
check "F: a real approval clears the stall"   "$(printf '%s' "$OUT" | jq -r '.approval_stalled')" "false"
check "F: ...and resets the counter"   "$(printf '%s' "$OUT" | jq -r '.passes_since_approval')" "0"

if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"
