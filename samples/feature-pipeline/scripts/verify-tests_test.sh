#!/bin/sh
# Tests for verify-tests.sh — the deterministic post-pass regression gate.
#
# Every case below is a regression test for a bug that actually deadlocked a
# production run (feature-pipeline instance b61b4f48): the loop spent 14
# consecutive passes blocking all 246 approvals on failures that did not exist.
#
# Verifies:
#   A. A clean tree reports no regression and blocks nothing.
#   B. A suite with build_ok:false at baseline is NOT treated as green. jq's `//`
#      operator fires on false as well as null, so `.build_ok // true` silently
#      turned a never-compiling suite into a green one; its guaranteed non-zero
#      exit then read as a fresh regression on every pass, forever.
#   C. Prose in an already-red suite ("Failed executing", "Failed to ...") never
#      becomes a blocking failure. Scraped names are advisory ONLY.
#   D. A genuine regression — a suite green at baseline now exiting non-zero —
#      IS caught, so the gate is not merely toothless.
#   E. `undefined:` / `error CS1234` inside ordinary PASSING output does not trip
#      build_broken; only anchored toolchain diagnostics on a failing suite do.
#   F. A missing baseline fails OPEN (blocks nothing) but reports low confidence.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/verify-tests.sh"

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

# Builds a workspace whose baseline mirrors the shape that broke the real run:
# one green suite, one already-red suite, one that never built.
setup() { # dir
  WS="$1"
  rm -rf "$WS"; mkdir -p "$WS/work"
  cat > "$WS/test-baseline.json" <<'JSON'
{"suites":[
 {"name":"go-unit","command":"sh ./go.sh","build_ok":true,"failed_count":0,"failing_tests":[]},
 {"name":"dotnet-api","command":"sh ./dn.sh","build_ok":true,"failed_count":92,"failing_tests":[]},
 {"name":"pg-integration","command":"sh ./pg.sh","build_ok":false,"failed_count":0,"failing_tests":[]}
]}
JSON
  # green suite, passes
  printf 'ok  \tshared/tools\t0.42s\n' > "$WS/work/go.sh"
  printf 'exit 0\n' >> "$WS/work/go.sh"
  # already-red suite: prose that the old scraper mined for fake test names
  cat > "$WS/work/dn.sh" <<'EOF'
echo "Failed executing the request pipeline"
echo "Failed to resolve service for type Foo"
echo "Failed! - Failed: 92, Passed: 1400"
exit 1
EOF
  # never built at baseline: compile error and non-zero exit, every single run
  cat > "$WS/work/pg.sh" <<'EOF'
echo "shared/adapters/postgres/mig.go:12:2: undefined: RunSchemaMigrations"
echo "FAIL	shared/adapters/postgres [build failed]"
exit 2
EOF
}

run() { # ws -> json on stdout
  printf '{"workspace":"%s","work_dir":"%s/work"}' "$1" "$1" | sh "$SCRIPT"
}

# --- Cases A, B, C: the deadlock shape must produce ZERO blocking failures ---
setup "$TMP/ws"
OUT="$(run "$TMP/ws")"
check "A/B/C: no blocking failures despite a non-building and an already-red suite" \
  "$(printf '%s' "$OUT" | jq -r '.new_failures')" "0"
check "B: a build_ok:false suite is not branded a regression" \
  "$(printf '%s' "$OUT" | jq -r '.regressed_suites | length')" "0"
check "B: its compile error does not trip build_broken" \
  "$(printf '%s' "$OUT" | jq -r '.build_broken')" "false"
check "C: scraped prose never reaches the blocking count" \
  "$(printf '%s' "$OUT" | jq -r '.new_failures')" "0"
check "measurement is high confidence" \
  "$(printf '%s' "$OUT" | jq -r '.confidence')" "high"

# --- Case D: a real regression in a green-at-baseline suite IS caught ---
setup "$TMP/ws2"
cat > "$TMP/ws2/work/go.sh" <<'EOF'
echo "--- FAIL: TestVariedAlbumsEnrolled (0.03s)"
echo "FAIL	shared/tools	0.42s"
exit 1
EOF
OUT2="$(run "$TMP/ws2")"
check "D: a green-at-baseline suite going red blocks" \
  "$(printf '%s' "$OUT2" | jq -r '.new_failures')" "1"
check "D: the regressed suite is named" \
  "$(printf '%s' "$OUT2" | jq -r '.regressed_suites[0]')" "go-unit"

# --- Case E: a real compile break in a suite that DID build at baseline ---
setup "$TMP/ws3"
cat > "$TMP/ws3/work/go.sh" <<'EOF'
echo "shared/tools/x.go:14:9: undefined: NewThing"
exit 2
EOF
check "E: genuine compile break in a baseline-building suite trips build_broken" \
  "$(run "$TMP/ws3" | jq -r '.build_broken')" "true"

setup "$TMP/ws4"
cat > "$TMP/ws4/work/go.sh" <<'EOF'
echo "ok  	shared/tools	0.42s"
echo "    logged: handler returned 'undefined: foo' as expected"
echo "    logged: error CS0103 quoted in a passing assertion"
exit 0
EOF
check "E: 'undefined:'/'error CS' in PASSING output does not trip build_broken" \
  "$(run "$TMP/ws4" | jq -r '.build_broken')" "false"

# --- Case F: no baseline -> fail open, but say so ---
setup "$TMP/ws5"
rm "$TMP/ws5/test-baseline.json"
OUT5="$(run "$TMP/ws5")"
check "F: a missing baseline blocks nothing" \
  "$(printf '%s' "$OUT5" | jq -r '.new_failures')" "0"
check "F: ...but reports low confidence" \
  "$(printf '%s' "$OUT5" | jq -r '.confidence')" "low"

if [ "$fails" -gt 0 ]; then
  echo "$fails check(s) failed"
  exit 1
fi
echo "all checks passed"
