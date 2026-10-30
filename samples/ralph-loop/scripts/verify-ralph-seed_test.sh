#!/bin/sh
# Tests for ralph-loop's PER-ROLE verify-ralph-seed.sh.
#
# The loop has three roles in one RALPH.md, and the failure mode this guards is
# specific: a file can carry a perfect protocol for one role and silently omit
# the self-assessment or stop rule for another. The missing one then surfaces
# sixty passes later as a timeout, which is exactly how a run was lost before.
#
# It also guards the plan SHAPE the 2026-09-11 redesign depends on:
#   - paired rows are charged at one pass each (96 rows took 66 passes; the old
#     "a third of a pass" charge let a 116-row plan through as 39 passes);
#   - a paired row is one seam, A <-> B, never a directional "A as impacted by B"
#     row with its mirror listed separately;
#   - checkbox syntax lives only in the two tables, because record-pass-commit
#     counts [x] file-wide and a checklist in a detail block would corrupt the
#     approval arithmetic;
#   - when the plan critic ran, RALPH.md must carry its "## Plan critique" record,
#     so a critic that silently did nothing cannot pass a plan into the loop.
set -eu
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/verify-ralph-seed.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1 (want $3, got $2)"; fails=$((fails+1)); fi; }
rejects() { # label json-extra
  if printf '{"work_dir":"%s","max_ralph_iterations":500%s}' "$TMP" "$2" | sh "$SCRIPT" >/dev/null 2>&1; then
    echo "FAIL - $1 was accepted"; fails=$((fails+1)); else echo "ok   - $1 is rejected"; fi; }

# The protocol the seeder actually tells the agent to transcribe.
awk '/---8<--- copy from here/{f=1;next} /---8<--- copy to here/{f=0} f' \
  "$SCRIPT_DIR/../templates/seed-ralph.md" > "$TMP/PROTO"
mkdir -p "$TMP/docs/reference"
plan() { # singles paired [paired-label]
  lab="${3:-a ↔ b}"
  { echo "## Single-System Reviews"; echo "| # | Focus Area | Risk | R1 | R2 |"; echo "|---|---|---|---|---|"
    i=1; while [ $i -le $1 ]; do echo "| $i | behaviour $i | high | [ ] | [ ] |"; i=$((i+1)); done
    echo; echo "## Area details"; echo "### Area 1 — behaviour 1"; echo "- **Entry:** x.go:Run"
    echo "- **Review 1 — wiring lens:**"; echo "  1. constructed at main.go:new"
  } > "$TMP/docs/reference/focus-areas.md"
  { echo "## Paired Integration Reviews"; echo "| # | Paired Review | Seam | Risk | Status |"; echo "|---|---|---|---|---|"
    i=1; while [ $i -le $2 ]; do echo "| $i | $lab | SHARED: t · TRACE: e to s · BREAK: f | high | [ ] |"; i=$((i+1)); done
    echo; echo "## No seam"; echo "- c and d: inside one behaviour (area 1)"
  } > "$TMP/docs/reference/paired-reviews.md"; }
run() { printf '{"work_dir":"%s","max_ralph_iterations":%s}' "$TMP" "$1" | sh "$SCRIPT" 2>/dev/null; }

# --- 1. the seeder's own protocol, a well-formed plan --------------------------
cp "$TMP/PROTO" "$TMP/RALPH.md"; plan 15 12
OUT="$(run 500)" && rc=0 || rc=1
check "the seeder's own protocol passes its verifier" "$rc" "0"
check "all three roles are recognised" "$(printf '%s' "$OUT" | jq -r '.roles|length')" "18"
check "paired rows are charged at ONE pass each (2*15 + 12)" "$(printf '%s' "$OUT" | jq -r '.implied_passes')" "42"
check "singles and paired counted" "$(printf '%s' "$OUT" | jq -r '"\(.singles)/\(.paired)"')" "15/12"
check "not critiqued when the critic did not run" "$(printf '%s' "$OUT" | jq -r '.critiqued')" "false"

# --- 2. the per-role protocol checks -----------------------------------------
sed '/Stop after the tag/d' "$TMP/PROTO" > "$TMP/RALPH.md"
rejects "a role missing its stop rule" ""
awk '/^## Role: single-review-2/{skip=1} /^## Role: paired-review/{skip=0} !skip' "$TMP/PROTO" > "$TMP/RALPH.md"
rejects "an entirely absent role block" ""

# --- 3. the budget -----------------------------------------------------------
cp "$TMP/PROTO" "$TMP/RALPH.md"; plan 60 80
rejects "a plan that cannot finish in its budget (2*60 + 80 = 200 > 175)" ""
plan 30 116
rejects "the old 116-row directional plan, which the third-of-a-pass charge let through" ""

# --- 4. plan shape -----------------------------------------------------------
plan 15 12 "a as impacted by b"
rejects "a directional paired row (the reverse-row format)" ""
plan 15 12
printf -- '- [ ] a stray checkbox in a detail block\n' >> "$TMP/docs/reference/focus-areas.md"
rejects "checkbox syntax outside a table" ""
plan 15 12
printf -- '- [x] a stray ticked box\n' >> "$TMP/docs/reference/paired-reviews.md"
rejects "a ticked box outside a table" ""

# --- 5. the critic's record ---------------------------------------------------
plan 15 12
rejects "a critiqued run whose RALPH.md carries no '## Plan critique' record" ',"critique-seed":{"response":"done"}'
printf '\n## Plan critique\n\n- areas 20 → 15, seams 30 → 12, implied passes 70 → 42, budget 175\n' >> "$TMP/RALPH.md"
OUT="$(printf '{"work_dir":"%s","max_ralph_iterations":500,"critique-seed":{"response":"done"}}' "$TMP" | sh "$SCRIPT" 2>/dev/null)" && rc=0 || rc=1
check "a critiqued run with the record passes" "$rc" "0"
check "and reports critiqued:true" "$(printf '%s' "$OUT" | jq -r '.critiqued')" "true"

# --- 6. the in-flight critic (replan=true on a resumed run) -------------------
plan 15 12; cp "$TMP/PROTO" "$TMP/RALPH.md"
rejects "a re-planned run whose RALPH.md carries no '## Plan critique' record" ',"critique-replan":{"response":"done"}'
printf '\n## Plan critique\n\n- replan 2026-09-11: areas 30 → 7, seams 20 → 10, ticked boxes 26 → 6\n' >> "$TMP/RALPH.md"
OUT="$(printf '{"work_dir":"%s","max_ralph_iterations":500,"critique-replan":{"response":"done"}}' "$TMP" | sh "$SCRIPT" 2>/dev/null)" && rc=0 || rc=1
check "a re-planned run with the record passes" "$rc" "0"
check "and reports critiqued:true for the replan critic" "$(printf '%s' "$OUT" | jq -r '.critiqued')" "true"

# --- 7. CHECK 5: seeded focus items must be named in the plan -------------------
plan 15 12; cp "$TMP/PROTO" "$TMP/RALPH.md"
mkdir -p "$TMP/.arbitron/pipeline"
SEED="$TMP/.arbitron/pipeline/s.survivors.json"
seed() { jq -n --args '{summary:"s", focus_items: ($ARGS.positional | map({id: .}))}' "$@" > "$SEED"; }
seeded() { printf '{"work_dir":"%s","max_ralph_iterations":500,"focus_seed":"%s"}' "$TMP" "$1"; }
printf '\n### Area 2 — limits (harden:pkg/a.go, harden:pkg/b.go)\n' >> "$TMP/docs/reference/focus-areas.md"

seed harden:pkg/a.go harden:pkg/b.go
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: all ids present ok" "$rc" "0"

seed harden:pkg/a.go harden:pkg/missing.go
ERR="$(seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" 2>&1 >/dev/null)" && rc=0 || rc=1
check "focus_seed: one missing exits 1" "$rc" "1"
case "$ERR" in *harden:pkg/missing.go*) echo "ok   - focus_seed: the missing id is named";; *) echo "FAIL - missing id not named: $ERR"; fails=$((fails+1));; esac
case "$ERR" in *harden:pkg/a.go*) echo "FAIL - a present id was reported missing"; fails=$((fails+1));; *) echo "ok   - focus_seed: present ids are not listed";; esac

seed harden:other
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: harden:other absent from the plan exits 1" "$rc" "1"
printf '\n### Area 3 — everything else (harden:other)\n' >> "$TMP/docs/reference/focus-areas.md"
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: harden:other handled once named" "$rc" "0"

seeded .arbitron/pipeline/nope.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: unreadable seed exits 1" "$rc" "1"
printf 'not json' > "$SEED"
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: malformed seed exits 1" "$rc" "1"

# an id is a whole token: a longer path sharing the prefix does not satisfy it
printf '\n### Area 4 — backups (harden:pkg/c.go.bak, x/harden:pkg/d.go)\n' >> "$TMP/docs/reference/focus-areas.md"
seed harden:pkg/c.go
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: harden:pkg/c.go is not satisfied by harden:pkg/c.go.bak" "$rc" "1"
seed harden:pkg/d.go
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: a longer prefix path does not satisfy the id" "$rc" "1"
printf '\n### Area 5 — end (harden:pkg/e.go).\n- harden:pkg/f.go' >> "$TMP/docs/reference/focus-areas.md"
seed harden:pkg/e.go harden:pkg/f.go
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: id before punctuation and at end-of-line matches" "$rc" "0"

# a shortfall with zero items is valid
printf '{"summary":"s","focus_items":[]}' > "$SEED"
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: empty focus_items passes" "$rc" "0"
printf '{"summary":"s"}' > "$SEED"
seeded .arbitron/pipeline/s.survivors.json | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: missing focus_items array exits 1" "$rc" "1"

seed harden:never/named.go
printf '{"work_dir":"%s","max_ralph_iterations":500,"focus_seed":""}' "$TMP" | sh "$SCRIPT" >/dev/null 2>&1 && rc=0 || rc=1
check "focus_seed: empty focus_seed skipped" "$rc" "0"

[ "$fails" -eq 0 ] || { echo "$fails check(s) failed"; exit 1; }
echo "all checks passed"
