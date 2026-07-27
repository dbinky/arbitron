#!/bin/sh
# Tests for verify-ralph-seed.sh — the guard that stops a RALPH.md without loop
# semantics from reaching the refinement loop.
#
# Covers all five scenario categories:
#   happy-path : the canonical ralph-plan template passes
#   success    : each required marker is individually load-bearing
#   failure    : the improvised no-terminator protocol is rejected
#   error      : missing RALPH.md / missing work_dir exit non-zero
#   edge-case  : reworded-but-complete passes; partial protocol is rejected
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/verify-ralph-seed.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { # desc actual expected
  if [ "$2" = "$3" ]; then echo "ok   - $1"; else
    echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails + 1))
  fi
}

W="$TMP/work"; mkdir -p "$W"
run() { printf '{"work_dir":"%s"}' "$W" | sh "$SCRIPT" 2>/dev/null; }
runs() { if printf '{"work_dir":"%s"}' "$W" | sh "$SCRIPT" >/dev/null 2>&1; then echo pass; else echo fail; fi; }

# A minimal but complete protocol — everything the loop needs to terminate.
complete_protocol() {
  cat > "$W/RALPH.md" <<'EOF'
# Loop Instructions
## Constraints
- One focus area per iteration. Fix the most important issue, then stop.
## Iteration Structure
7. Assess the checklist, then proceed to Wrap Up regardless of checklist state.
## Wrap Up
Step C - Commit and push.
Then output exactly one tag:
- `<promise>FINIT</promise>`
- `<promise>CLOSER</promise>`
Stop after the tag every time.
EOF
}

# ---------------------------------------------------------------- happy path
CANON="$HOME/.claude/skills/ralph-plan/docs/templates/RALPH.md"
if [ -f "$CANON" ]; then
  cp "$CANON" "$W/RALPH.md"
  check "happy: canonical ralph-plan template passes" "$(runs)" "pass"
  check "happy: reports ok:true"                      "$(run | jq -r .ok)" "true"
else
  echo "skip - canonical template not installed at $CANON"
fi

# ------------------------------------------------------------------- success
# Every marker must be load-bearing: drop one at a time, expect rejection.
for marker in '<promise>FINIT</promise>' '<promise>CLOSER</promise>' 'Wrap Up' \
              'Stop after the tag' 'then stop' 'proceed to Wrap Up'; do
  complete_protocol
  grep -vF -- "$marker" "$W/RALPH.md" > "$W/tmp" && mv "$W/tmp" "$W/RALPH.md"
  check "success: removing '$marker' is detected" "$(runs)" "fail"
done

complete_protocol
check "success: intact protocol passes" "$(runs)" "pass"

# ------------------------------------------------------------------- failure
# The real-world defect: a protocol that says what to do and never how to stop.
cat > "$W/RALPH.md" <<'EOF'
# RALPH
## Loop protocol
1. Read tracking files.
2. Pick a focus area.
3. Make the change.
4. Run tests.
5. Update tracking.
6. Commit and push.
EOF
check "failure: improvised protocol with no terminator is rejected" "$(runs)" "fail"

# --------------------------------------------------------------------- error
rm -f "$W/RALPH.md"
check "error: missing RALPH.md exits non-zero" "$(runs)" "fail"

if printf '{}' | sh "$SCRIPT" >/dev/null 2>&1; then
  check "error: missing work_dir exits non-zero" "pass" "fail"
else
  check "error: missing work_dir exits non-zero" "fail" "fail"
fi

# ----------------------------------------------------------------- edge case
# Reworded prose around the load-bearing markers must still pass — the guard
# checks for terminators, not for a byte-identical document.
cat > "$W/RALPH.md" <<'EOF'
# Refinement charter for the widgets feature
Goal: finish the widgets work.
## Constraints
Handle exactly one focus area per pass; fix the single most important issue, then stop.
## Iteration Structure
Finally, assess the checklist and proceed to Wrap Up no matter what it says.
## Wrap Up
Commit and push, then emit `<promise>FINIT</promise>` if everything is complete,
otherwise `<promise>CLOSER</promise>`. Stop after the tag every time.
EOF
check "edge: reworded but complete protocol passes" "$(runs)" "pass"

# Half a protocol — tags present, no stop rule — is the subtlest failure.
cat > "$W/RALPH.md" <<'EOF'
## Wrap Up
Emit `<promise>FINIT</promise>` or `<promise>CLOSER</promise>`.
EOF
check "edge: tags without a stop rule are rejected" "$(runs)" "fail"

echo
if [ "$fails" -eq 0 ]; then
  echo "all verify-ralph-seed tests passed"
else
  echo "$fails verify-ralph-seed test(s) FAILED"
  exit 1
fi
