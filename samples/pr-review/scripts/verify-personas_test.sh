#!/bin/sh
# Tests for verify-personas.sh — the persona-completeness gate before the manager.
#
# Verifies (no network: `gh` is stubbed on PATH and prints canned comment bodies):
#   A. Every required persona posted => exit 0, {personas_ok:true}.
#   B. security posted, security2 did not => exit 1, stderr names security2
#      (the incident this gate exists for).
#   C. security2 posted, security did not => exit 1, stderr names security
#      (^security: must not match security2: lines).
#   D. security posted, security2 did not, and "security:" text only appears
#      mid-line => still exit 1 (prefix is anchored).
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/verify-personas.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Stub gh: `auth status` succeeds; `api .../pulls/...` prints $TMP/inline,
# `api .../issues/...` prints $TMP/general (one comment body per line, as the
# real call's --jq '.[].body' would).
mkdir -p "$TMP/bin"
cat > "$TMP/bin/gh" <<STUB
#!/bin/sh
case "\$1" in
  auth) exit 0 ;;
  api)
    case "\$2" in
      */pulls/*) cat "$TMP/inline" ;;
      */issues/*) cat "$TMP/general" ;;
    esac ;;
esac
STUB
chmod +x "$TMP/bin/gh"

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

# run_gate <inline bodies> <general bodies>: sets rc, out, err.
run_gate() {
  printf '%s\n' "$1" > "$TMP/inline"
  printf '%s\n' "$2" > "$TMP/general"
  rc=0
  out="$(printf '{"repo":"x/y","pr_number":1}' | PATH="$TMP/bin:$PATH" sh "$SCRIPT" 2>"$TMP/err")" || rc=$?
  err="$(cat "$TMP/err")"
}
names() { # does stderr's DROPOUT line name persona $1 as missing?
  printf '%s\n' "$err" | grep 'PERSONA DROPOUT' | grep -Eq "[: ]$1( |\.|$)" && echo yes || echo no
}

# --- Case A: full panel ---
run_gate "architect: [BUG] a
10x: AGREE
security: [HIGH] x" "security2: [INFO] y"
check "full panel => exit 0" "$rc" "0"
check "full panel => personas_ok:true" "$(printf '%s' "$out" | jq -r '.personas_ok')" "true"

# --- Case B: security present, security2 absent ---
run_gate "architect: a
10x: b
security: [HIGH] c" "security: [LOW] d"
check "security2 absent => exit 1" "$rc" "1"
check "security2 absent => stderr names security2" "$(names security2)" "yes"
check "security2 absent => security not reported missing" "$(names security)" "no"

# --- Case C: security2 present, security absent ---
run_gate "architect: a
10x: b
security2: [CRITICAL] c" ""
check "security absent => exit 1" "$rc" "1"
check "security absent => stderr names security" "$(names security)" "yes"
check "security absent => security2 not reported missing" "$(names security2)" "no"

# --- Case D: persona prefixes are anchored to line start ---
run_gate "architect: a
10x: b
security: c
manager: see security2: thread" ""
check "mid-line security2: does not count => exit 1" "$rc" "1"
check "mid-line security2: does not count => names security2" "$(names security2)" "yes"

echo "---"
if [ "$fails" -eq 0 ]; then
  echo "PASS"
else
  echo "$fails check(s) FAILED"
  exit 1
fi
