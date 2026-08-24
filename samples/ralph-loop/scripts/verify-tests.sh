#!/bin/sh
# Deterministic post-pass regression gate: compares the suites against the
# branch-point baseline, running only as much of them as the pass's diff warrants.
#
# WHY THIS EXISTS: nothing in the loop verified a pass's test claims. The agent
# that made the change also narrated the result, and refine-assess is toolless,
# so "no NEW failures vs baseline" was decided from prose written by the author
# of the change.
#
# WHY THE BLOCKING RULE IS EXIT-CODE ONLY (run b61b4f48): the first version
# scraped test names out of mixed Go/.NET/Flutter/vitest output and fed the count
# into a hard block. `FAILED|Failed` plus any word yielded "to", "executing",
# "defaults"; a Go duration suffix yielded "(0.00s)". Worse, jq's `//` fires on
# false as well as null, so `.build_ok // true` turned a suite that never
# compiled into a "green at baseline" one and branded it a regression forever.
# 14 passes blocked all 246 approvals while the agents correctly identified the
# artifacts and were overruled. The blocking signal is now only what needs no
# parsing: a suite that BUILT and was FULLY GREEN at baseline now exits non-zero.
#
# WHY IT IS NOW SCOPED (measured over 87 passes): running all 11 suites every
# pass cost 14.3 min — MORE wall-clock than the refine agent itself (7.7 min),
# and 1,246 of the run's minutes in total. Most of it re-proved that untouched
# packages were still fine. Worse, the paired-integration phase is 160 of 248
# reviews and is largely INSPECTION: those passes change no executable code at
# all and were still paying the full tax.
#
# Three tiers, chosen from the pass's own diff:
#   skipped — nothing executable changed; the baseline comparison is trivially
#             satisfied because nothing that could fail moved.
#   scoped  — run only the suites whose paths the diff touched.
#   full    — every Nth pass, when a changed path maps to no known suite, or when
#             the diff cannot be determined. Failure mode is always "test too
#             much", never "test too little".
#
# Reads {workspace, work_dir} from stdin JSON. Emits:
#   tests_ran / scope / suites_run / suites_skipped
#   new_failures        number — BLOCKING: green-at-baseline suites now failing
#   regressed_suites    array
#   build_broken        bool   — BLOCKING: a target that compiled no longer does
#   suspected_new_tests array  — ADVISORY only; scraped, may contain artifacts
#   advisory_note / confidence / verdict
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || WORK="$WS/work"
BASE="$WS/test-baseline.json"
FULL_SWEEP_EVERY="${FULL_SWEEP_EVERY:-10}"
TMP="${TMPDIR:-/tmp}/verify-tests.$$"
mkdir -p "$TMP"
trap 'rm -rf "$TMP"' EXIT

emit() { # ran nf regressed build_broken suspected advisory confidence scope run skipped summary
  jq -n --argjson ran "$1" --argjson nf "$2" --argjson rs "$3" --argjson bb "$4" \
        --argjson st "$5" --arg an "$6" --arg c "$7" --arg sc "$8" \
        --argjson sr "$9" --argjson sk "${10}" --arg s "${11}" \
    '{tests_ran:$ran, new_failures:$nf, regressed_suites:$rs, build_broken:$bb,
      suspected_new_tests:$st, advisory_note:$an, confidence:$c,
      scope:$sc, suites_run:$sr, suites_skipped:$sk, verdict:$s}'
}

if [ ! -f "$BASE" ] || ! jq -e 'type=="object" and (.suites|type=="array") and (.suites|length>0)' "$BASE" >/dev/null 2>&1; then
  emit false 0 '[]' false '[]' "No usable baseline, so nothing can be compared." low \
    none '[]' '[]' \
    "NOT MEASURED: no usable baseline at $BASE. A regression cannot be distinguished from a pre-existing failure, so nothing is blocked. Treat this pass's test evidence as UNVERIFIED and confirm by hand before approving."
  exit 0
fi

# --- what did this pass change? ----------------------------------------------
# record-pass-commit runs AFTER this node, so last-pass-head still points at the
# previous pass's HEAD — exactly the diff base we want.
PREV=""
[ -f "$WS/last-pass-head" ] && PREV="$(cat "$WS/last-pass-head" 2>/dev/null || true)"
CHANGED="$TMP/changed"; : > "$CHANGED"
DIFF_OK=false
if [ -n "$PREV" ] && git -C "$WORK" cat-file -e "${PREV}^{commit}" 2>/dev/null; then
  if git -C "$WORK" diff --name-only "${PREV}..HEAD" > "$CHANGED" 2>/dev/null; then DIFF_OK=true; fi
fi

# Tracking/prose files cannot change a test outcome.
EXEC_CHANGED="$TMP/exec"; : > "$EXEC_CHANGED"
if [ "$DIFF_OK" = true ]; then
  grep -vE '^(RALPH\.md$|docs/|.*\.md$)' "$CHANGED" > "$EXEC_CHANGED" 2>/dev/null || true
fi

# --- full-sweep cadence -------------------------------------------------------
SWEEP="$WS/verify-sweep-counter"
N=0
[ -f "$SWEEP" ] && N="$(cat "$SWEEP" 2>/dev/null || echo 0)"
case "$N" in ''|*[!0-9]*) N=0 ;; esac
N=$((N + 1))

SCOPE=full
if [ "$DIFF_OK" != true ]; then
  SCOPE=full                                   # cannot tell -> test everything
elif [ "$N" -ge "$FULL_SWEEP_EVERY" ]; then
  SCOPE=full
elif [ ! -s "$EXEC_CHANGED" ]; then
  SCOPE=skipped                                # inspection-only pass
else
  SCOPE=scoped
fi
[ "$SCOPE" = full ] && N=0
printf '%s' "$N" > "$SWEEP"

# A suite "owns" a path prefix if its command names it: `cd <dir>` or `./dir/...`.
# Everything is best-effort; anything unrecognised forces a full run.
prefixes_for() { # command -> newline-separated path prefixes
  printf '%s\n' "$1" \
    | tr ' ' '\n' \
    | sed -n -e 's|^\./\([A-Za-z0-9_.-][A-Za-z0-9_./-]*\)/\.\.\.$|\1|p' \
             -e 's|^\./\([A-Za-z0-9_.-][A-Za-z0-9_./-]*\)/*$|\1|p' \
    | sed 's|/*$||' | grep -v '^$' | sort -u
  printf '%s\n' "$1" | sed -n 's|.*cd \([A-Za-z0-9_.-][A-Za-z0-9_./-]*\).*|\1|p' | sed 's|/*$||'
}

REG="$TMP/regressed"; : > "$REG"
SUS="$TMP/suspected"; : > "$SUS"
RAN_L="$TMP/ran"; : > "$RAN_L"
SKIP_L="$TMP/skipped"; : > "$SKIP_L"
BUILD_BROKEN=false
RAN=false
ALREADY_RED=0
UNMAPPED=false
NS="$(jq -r '.suites | length' "$BASE")"

scrape() {
  {
    sed -n 's/^[[:space:]]*--- FAIL: \([A-Za-z_][A-Za-z0-9_]*\(\/[A-Za-z0-9_.-]\{1,\}\)*\).*/\1/p' "$1"
    sed -n 's/^[[:space:]]*\(Failed\|\[FAIL\]\)[[:space:]]\{1,\}\([A-Za-z_][A-Za-z0-9_]*\(\.[A-Za-z_][A-Za-z0-9_]*\)\{1,\}\).*/\2/p' "$1"
    sed -n 's/^FAILED[[:space:]]\{1,\}\([^[:space:]]\{1,\}::[^[:space:]]\{1,\}\).*/\1/p' "$1"
    sed -n 's/^[[:space:]]*[✕×][[:space:]]\{1,\}\(.\{3,\}\)$/\1/p' "$1"
  } 2>/dev/null | sed 's/[[:space:]]*$//' \
    | grep -vE '^\(.*\)$' \
    | grep -vE '^(to|executing|defaults|deck|the|a|an|and|or|of|in|at|is|was|for|with|from)$' \
    | grep -E '.{3,}' | sort -u
}

# Pre-pass: if any changed executable path maps to NO suite, widen to full.
if [ "$SCOPE" = scoped ]; then
  ALLPFX="$TMP/allpfx"; : > "$ALLPFX"
  i=0
  while [ "$i" -lt "$NS" ]; do
    CMD="$(jq -r ".suites[$i].command // empty" "$BASE")"; i=$((i + 1))
    [ -n "$CMD" ] && prefixes_for "$CMD" >> "$ALLPFX"
  done
  sort -u "$ALLPFX" -o "$ALLPFX"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    hit=false
    while IFS= read -r p; do
      [ -n "$p" ] || continue
      case "$f" in "$p"/*|"$p") hit=true; break ;; esac
    done < "$ALLPFX"
    [ "$hit" = false ] && { UNMAPPED=true; break; }
  done < "$EXEC_CHANGED"
  [ "$UNMAPPED" = true ] && SCOPE=full
fi

i=0
while [ "$i" -lt "$NS" ]; do
  CMD="$(jq -r ".suites[$i].command // empty" "$BASE")"
  NAME="$(jq -r ".suites[$i].name // \"suite-$i\"" "$BASE")"
  BFAIL="$(jq -r ".suites[$i].failed_count // 0" "$BASE")"
  # NB: jq's `//` fires on false as well as null, so `.build_ok // true` silently
  # turns a non-building suite into a green one. That single operator branded
  # pg-integration a fresh regression on every pass of run b61b4f48.
  BBUILD="$(jq -r ".suites[$i] | if has(\"build_ok\") then (.build_ok|tostring) else \"true\" end" "$BASE")"
  i=$((i + 1))
  [ -n "$CMD" ] || continue

  WANT=true
  if [ "$SCOPE" = skipped ]; then
    WANT=false
  elif [ "$SCOPE" = scoped ]; then
    WANT=false
    PFX="$(prefixes_for "$CMD")"
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      while IFS= read -r p; do
        [ -n "$p" ] || continue
        case "$f" in "$p"/*|"$p") WANT=true; break ;; esac
      done <<EOF
$PFX
EOF
      [ "$WANT" = true ] && break
    done < "$EXEC_CHANGED"
  fi

  if [ "$WANT" != true ]; then
    printf '%s\n' "$NAME" >> "$SKIP_L"
    continue
  fi

  OUT="$TMP/out.$i"
  RAN=true
  printf '%s\n' "$NAME" >> "$RAN_L"
  rc=0
  ( cd "$WORK" && sh -c "$CMD" ) > "$OUT" 2>&1 || rc=$?

  if [ "$BBUILD" = "true" ] && [ "$BFAIL" -eq 0 ]; then
    if [ "$rc" -ne 0 ]; then
      printf '%s\n' "$NAME" >> "$REG"
      scrape "$OUT" | sed "s|^|$NAME: |" >> "$SUS"
    fi
    if [ "$rc" -ne 0 ] && grep -qE '^[^[:space:]]+\.go:[0-9]+:[0-9]+: |^[^[:space:]]+\.cs\([0-9]+,[0-9]+\): error CS[0-9]+|^Build FAILED\.|^[[:space:]]*Error: Compilation failed' "$OUT" 2>/dev/null; then
      BUILD_BROKEN=true
    fi
  else
    ALREADY_RED=$((ALREADY_RED + 1))
    scrape "$OUT" | sed "s|^|$NAME (already red at baseline): |" >> "$SUS"
  fi
done

j() { sed '/^[[:space:]]*$/d' "$1" | jq -R -s 'split("\n")|map(select(length>0))'; }
REG_JSON="$(j "$REG")"; SUS_JSON="$(sed '/^[[:space:]]*$/d' "$SUS" | head -40 | jq -R -s 'split("\n")|map(select(length>0))')"
RAN_JSON="$(j "$RAN_L")"; SKIP_JSON="$(j "$SKIP_L")"
NF="$(printf '%s' "$REG_JSON" | jq 'length')"
NRUN="$(printf '%s' "$RAN_JSON" | jq 'length')"

CONF=high
[ "$SCOPE" = skipped ] && CONF=high      # nothing executable changed: a real conclusion
[ "$SCOPE" = scoped ] && CONF=high

AN="Names above are SCRAPED from mixed-toolchain output and are ADVISORY ONLY — they include fragments that are not test identifiers, and they never block an approval. The blocking signal is new_failures (suites that built and were fully green at baseline and now exit non-zero) and build_broken."
[ "$ALREADY_RED" -gt 0 ] && AN="$AN $ALREADY_RED suite(s) were already red or non-building at baseline; extra failures inside them cannot be detected by exit code and are not blocked."

case "$SCOPE" in
  skipped) SCOPEMSG="SCOPE=skipped: this pass changed no executable file (tracking/prose only), so no suite could have changed outcome. A full sweep runs at least every $FULL_SWEEP_EVERY passes." ;;
  scoped)  SCOPEMSG="SCOPE=scoped: ran $NRUN of $NS suite(s) — only those owning a path this pass touched. A full sweep runs at least every $FULL_SWEEP_EVERY passes." ;;
  *)       SCOPEMSG="SCOPE=full: ran all $NRUN of $NS suite(s)." ;;
esac

if [ "$NF" -eq 0 ] && [ "$BUILD_BROKEN" = false ]; then
  SUM="NO REGRESSION: every suite checked that built and was fully green at the branch point still exits clean. Pre-existing failures are unchanged and are not this branch's job. Nothing blocks approval on test grounds. $SCOPEMSG"
else
  SUM="REGRESSION: $NF suite(s) that were green at the branch point now fail: $(tr '\n' ' ' < "$REG")."
  [ "$BUILD_BROKEN" = true ] && SUM="$SUM A target that COMPILED at baseline no longer compiles."
  SUM="$SUM Approvals are blocked until this is fixed or explicitly reverted. $SCOPEMSG"
fi

emit "$RAN" "$NF" "$REG_JSON" "$BUILD_BROKEN" "$SUS_JSON" "$AN" "$CONF" "$SCOPE" "$RAN_JSON" "$SKIP_JSON" "$SUM"
