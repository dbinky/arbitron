#!/bin/sh
# Applies the tamper judge's verdict (or a forced void) to work_dir.
#
#   tamper-verdict.sh <base> [--force-void] [--reset] [--restore <path>]...
#                     [--restore-json <dotted.stdin.path>]... [--gaps-file <path>]
#                     [--clear <workspace-relative path>]... [--reason <word>]
#
# Reads the snapshot JSON on stdin (work_dir, workspace, pass-mark.pass_id, plus
# any key a --restore-json path names) and writes exactly one JSON object to
# stdout; diagnostics go to stderr. Exit 1 means bad inputs or a git failure.
#
# Voids when --force-void is given, findings.json reports force_void, or the
# verdict (<workspace>/tamper/verdict.json) is missing, malformed, lacks a ruling
# for some finding id, or holds a ruling with legitimate:false (fail closed).
# A forced void, a history rewrite and --reset hard-reset work_dir to <base>.
# Any other void restores test, test-config, mutation-config, CI and baseline
# files changed in <base>..HEAD (plus --restore paths) and commits only those
# paths, so unrelated working-tree edits survive. --restore-json is a dotted
# path, split on "." and read with jq getpath, so hyphenated ids work.
# --reason (only with --force-void) replaces "force_void" as the reported reason,
# so must-fix.md says why a caller voided a pass without running the gate.
set -eu

die() { echo "tamper-verdict: $*" >&2; exit 1; }
die2() { echo "tamper-verdict: $*" >&2; exit 2; }

# unsafe <path>: absolute, or any ".." component.
unsafe() {
  case "$1" in /*|..|../*|*/..|*/../*) return 0 ;; esac
  return 1
}

BASE="${1:-}"
[ -n "$BASE" ] || die "usage: tamper-verdict.sh <base> [flags]"
shift

FORCE=0
RESET_MODE=""
RESET=0
GAPS=""
FORCE_REASON=""
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
: > "$TMP/restore.txt"
: > "$TMP/restorejson.txt"
: > "$TMP/clear.txt"

while [ $# -gt 0 ]; do
  case "$1" in
    --force-void) FORCE=1 ;;
    --reset) RESET=1 ;;
    --restore) [ $# -ge 2 ] || die "--restore needs a path"; unsafe "$2" && die2 "unsafe --restore path: $2"; printf '%s\n' "$2" >> "$TMP/restore.txt"; shift ;;
    --restore-json) [ $# -ge 2 ] || die "--restore-json needs a path"; printf '%s\n' "$2" >> "$TMP/restorejson.txt"; shift ;;
    --gaps-file) [ $# -ge 2 ] || die "--gaps-file needs a path"; unsafe "$2" && die2 "unsafe --gaps-file path: $2"; GAPS="$2"; shift ;;
    --reason) [ $# -ge 2 ] || die "--reason needs a word"
      case "$2" in ''|*[!a-z0-9_]*) die "--reason must match [a-z0-9_]+: $2" ;; esac
      FORCE_REASON="$2"; shift ;;
    --clear) [ $# -ge 2 ] || die "--clear needs a path"; unsafe "$2" && die2 "unsafe --clear path: $2"; printf '%s\n' "$2" >> "$TMP/clear.txt"; shift ;;
    *) die "unknown argument: $1" ;;
  esac
  shift
done

[ -z "$FORCE_REASON" ] || [ "$FORCE" = 1 ] || die "--reason needs --force-void"

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""' 2>/dev/null)" || die "stdin is not JSON"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
PASS_ID="$(printf '%s' "$IN" | jq -c '.["pass-mark"].pass_id // null')"

[ -n "$WORK" ] || die "work_dir not set"
[ -n "$WS" ] || die "workspace not set"
# The repository belongs to the agent being checked: ignore its replace refs so
# the restore below reads the committed blobs, as tamper-gate.sh does.
GIT_NO_REPLACE_OBJECTS=1
export GIT_NO_REPLACE_OBJECTS
git -C "$WORK" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not a git worktree: $WORK"
BASE_SHA="$(git -C "$WORK" rev-parse --verify -q "$BASE^{commit}" 2>/dev/null)" || die "base is not a commit: $BASE"
HEAD_SHA="$(git -C "$WORK" rev-parse HEAD)"

g() { git -C "$WORK" -c core.quotepath=off "$@"; }

FINDINGS="$WS/tamper/findings.json"
VERDICT="$WS/tamper/verdict.json"
MUSTFIX="$WS/tamper/must-fix.md"

# --- decide ------------------------------------------------------------------
VOIDED=0
REASON=""
HIST=false
printf '[]' > "$TMP/findings-list.json"
SUMMARY='{"findings":0,"legitimate":0,"illegitimate":0,"unruled":0}'

if [ -f "$FINDINGS" ] && jq -e 'type == "object"' "$FINDINGS" >/dev/null 2>&1; then
  HIST="$(jq -r '(.history_rewritten // false)' "$FINDINGS")"
  jq -c '(.findings // [])' "$FINDINGS" > "$TMP/findings-list.json"
  NF="$(jq 'length' "$TMP/findings-list.json")"
  FFV="$(jq -r '(.force_void // false)' "$FINDINGS")"
  SC="$(jq -r '(.source_changed // false)' "$FINDINGS")"
fi
NF="${NF:--1}"
FFV="${FFV:-false}"
SC="${SC:-false}"

if [ "$FORCE" = 1 ]; then
  VOIDED=1; REASON="${FORCE_REASON:-force_void}"
elif [ "$HIST" = true ]; then
  VOIDED=1; REASON="history_rewritten"
elif [ "$FFV" = true ]; then
  VOIDED=1
  if [ "$SC" = true ]; then REASON="source_changed"; else REASON="force_void"; fi
elif [ "$NF" = 0 ]; then
  : # nothing was flagged: the pass stands without a verdict
elif [ ! -f "$FINDINGS" ] || ! jq -e 'type == "object"' "$FINDINGS" >/dev/null 2>&1; then
  VOIDED=1; REASON="findings_missing"
elif [ ! -f "$VERDICT" ]; then
  VOIDED=1; REASON="verdict_missing"
elif ! jq -e '(.rulings | type == "array") and all(.rulings[]; (type == "object") and (.id | type == "string") and (.legitimate | type == "boolean"))' "$VERDICT" >/dev/null 2>&1; then
  VOIDED=1; REASON="verdict_malformed"
fi

# Rulings by id: the last ruling for an id wins.
if [ -f "$VERDICT" ] && jq -e '(.rulings | type == "array")' "$VERDICT" >/dev/null 2>&1 && [ "$REASON" != verdict_malformed ]; then
  RULINGS="$(jq -c '.rulings' "$VERDICT")"
else
  RULINGS='[]'
fi

# Per finding: ruled legitimate, ruled illegitimate (with reason), or unruled.
jq -n --slurpfile F "$TMP/findings-list.json" --argjson R "$RULINGS" --arg why "$REASON" '
  [ $F[0][] as $f
    | ([$R[] | select(.id == $f.id)] | last) as $r
    | $f + { ruling: (if $r == null then "unruled" elif $r.legitimate then "legitimate" else "illegitimate" end),
             ruling_reason: (if $r != null then ($r.reason // "")
                             elif $f.judgeable == false then "not judgeable: \($f.detail // $f.kind)"
                             elif $why != "" and $why != "verdict_missing" and $why != "verdict_malformed" and $why != "findings_missing"
                               then "pass voided without judging (\($why)): \($f.detail // $f.kind)"
                             else "no ruling given" end) } ]
' > "$TMP/judged.json"

SUMMARY="$(jq -c '{findings: length,
  legitimate: ([.[] | select(.ruling == "legitimate")] | length),
  illegitimate: ([.[] | select(.ruling == "illegitimate")] | length),
  unruled: ([.[] | select(.ruling == "unruled")] | length)}' "$TMP/judged.json")"

if [ "$VOIDED" = 0 ]; then
  if [ "$(printf '%s' "$SUMMARY" | jq '.unruled')" -gt 0 ]; then
    VOIDED=1; REASON="unruled_findings"
  elif [ "$(printf '%s' "$SUMMARY" | jq '.illegitimate')" -gt 0 ]; then
    VOIDED=1; REASON="illegitimate_ruling"
  fi
fi

emit() {
  # $1 restored files JSON array, $2 must-fix path
  jq -n --argjson voided "$( [ "$VOIDED" = 1 ] && echo true || echo false )" \
    --arg reason "$REASON" --arg base "$BASE" --argjson pass_id "$PASS_ID" \
    --argjson restored "$1" --arg mf "$2" --argjson summary "$SUMMARY" --arg rm "$RESET_MODE" \
    '{reset_mode: $rm, voided: $voided, reason: $reason, pass_base: $base, pass_id: $pass_id,
      restored_files: $restored, must_fix_path: $mf, rulings_summary: $summary}'
}

# --- pass stands -------------------------------------------------------------
if [ "$VOIDED" = 0 ]; then
  rm -f "$MUSTFIX"
  emit '[]' ""
  exit 0
fi

# --- void --------------------------------------------------------------------
RESTORED='[]'

HEAD8="$(printf '%s' "$HEAD_SHA" | cut -c1-8)"
BASE8="$(printf '%s' "$BASE_SHA" | cut -c1-8)"

if [ "$RESET" = 1 ] || [ "$FORCE" = 1 ] || [ "$FFV" = true ] || [ "$HIST" = true ]; then
  # --keep preserves dirty tracked edits that are not from the pass; it
  # refuses on conflicts, and then --hard is the fallback.
  if g reset -q --keep "$BASE_SHA" 2>/dev/null; then
    RESET_MODE=keep
  else
    echo "tamper-verdict: reset --keep refused; falling back to --hard" >&2
    g reset -q --hard "$BASE_SHA" || die "git reset --hard failed"
    RESET_MODE=hard_fallback
  fi
  if [ -n "$GAPS" ]; then
    mkdir -p "$(dirname "$WORK/$GAPS")"
    jq -r '.[] | select(.ruling != "legitimate")
      | "- [ ] TAMPER \(.id) (\(.kind), \(.file)): \(.ruling_reason)"' "$TMP/judged.json" >> "$WORK/$GAPS"
    g add -- "$GAPS"
    if ! g diff --cached --quiet HEAD -- "$GAPS"; then
      g commit -q --no-verify -m "chore(tamper): record voided pass $BASE8..$HEAD8" -- "$GAPS" || die "gaps commit failed"
    fi
  fi
else
  # Gated paths: test/config/ci roles from the gate's classifier, plus baselines.
  SELF_DIR="$(dirname "$0")"
  printf '{"work_dir":%s}' "$(printf '%s' "$WORK" | jq -Rs .)" \
    | sh "$SELF_DIR/tamper-gate.sh" --classify "$BASE_SHA" "$HEAD_SHA" > "$TMP/classify.json" \
    || die "classify failed"
  jq -r '.files[] | select(.role == "test" or .role == "config" or .role == "ci") | .path' "$TMP/classify.json" > "$TMP/gated.txt"
  g diff --no-renames --name-only "$BASE_SHA" "$HEAD_SHA" \
    | grep -E '\.(baseline|harden|survivors)\.json$|(^|/)test-baseline\.json$' >> "$TMP/gated.txt" || true
  # Old side of a renamed gated file.
  cp "$TMP/gated.txt" "$TMP/gated0.txt"
  g diff --name-status -M "$BASE_SHA" "$HEAD_SHA" | while IFS="$(printf '\t')" read -r st old new; do
    case "$st" in R*) if grep -Fxq -- "$new" "$TMP/gated0.txt"; then printf '%s\n' "$old"; fi ;; esac
  done > "$TMP/renamed.txt"
  cat "$TMP/renamed.txt" >> "$TMP/gated.txt"

  cat "$TMP/restore.txt" >> "$TMP/gated.txt"
  while IFS= read -r spec; do
    [ -n "$spec" ] || continue
    printf '%s' "$IN" | jq -r --arg p "$spec" '
      getpath($p | split(".")) as $v
      | if $v == null then empty elif ($v | type) == "array" then $v[] else $v end
      | select(type == "string")' >> "$TMP/gated.txt"
  done < "$TMP/restorejson.txt"

  sort -u "$TMP/gated.txt" | grep -v '^$' > "$TMP/paths.txt" || true
  while IFS= read -r p; do
    ! unsafe "$p" || die2 "unsafe restore path: $p"
  done < "$TMP/paths.txt"

  : > "$TMP/touched.txt"
  while IFS= read -r p; do
    if g cat-file -e "$BASE_SHA:$p" 2>/dev/null; then
      g checkout "$BASE_SHA" -- "$p" || die "restore failed: $p"
    elif g cat-file -e "HEAD:$p" 2>/dev/null; then
      g rm -q -f -- "$p" || die "remove failed: $p"
    else
      continue
    fi
    printf '%s\n' "$p" >> "$TMP/touched.txt"
  done < "$TMP/paths.txt"

  # Which touched paths actually differ from HEAD (staged by checkout/rm)?
  : > "$TMP/changed.txt"
  while IFS= read -r p; do
    if ! g diff --cached --quiet HEAD -- "$p"; then printf '%s\n' "$p" >> "$TMP/changed.txt"; fi
  done < "$TMP/touched.txt"
  RESTORED="$(jq -R . < "$TMP/changed.txt" | jq -sc .)"

  # must-fix and gaps lines are built below; the gaps file rides in the commit.
  if [ -n "$GAPS" ]; then
    mkdir -p "$(dirname "$WORK/$GAPS")"
    jq -r '.[] | select(.ruling != "legitimate")
      | "- [ ] TAMPER \(.id) (\(.kind), \(.file)): \(.ruling_reason)"' "$TMP/judged.json" >> "$WORK/$GAPS"
    g add -- "$GAPS"
    printf '%s\n' "$GAPS" >> "$TMP/changed.txt"
  fi

  if [ -s "$TMP/changed.txt" ]; then
    g commit -q --no-verify -m "revert(tamper): void pass $BASE8..$HEAD8" --pathspec-from-file="$TMP/changed.txt" \
      || die "revert commit failed"
  fi
fi

# --- after a void ------------------------------------------------------------
while IFS= read -r c; do
  [ -n "$c" ] || continue
  rm -rf "${WS:?}/$c"
done < "$TMP/clear.txt"

mkdir -p "$WS/tamper"
{
  printf '# MUST FIX before anything else this pass\n\n'
  printf 'The previous pass (%s..%s) was voided: %s.\n\n' "$BASE8" "$HEAD8" "$REASON"
  if [ "$(jq 'length' "$TMP/judged.json")" -gt 0 ]; then
    jq -r '.[] | select(.ruling != "legitimate")
      | "- \(.id) (\(.kind), \(.file)): \(.ruling_reason)"' "$TMP/judged.json"
  else
    printf -- '- No judgeable finding; the pass was voided as a whole (%s).\n' "$REASON"
  fi
  # Restored files not covered by a flagged finding: legitimate work rolled back.
  printf '%s' "$RESTORED" | jq -r --slurpfile J "$TMP/judged.json" '
    [$J[0][] | select(.ruling != "legitimate") | .file] as $bad
    | .[] | select(. as $p | $bad | index($p) | not)' > "$TMP/collateral.txt"
  if [ -s "$TMP/collateral.txt" ]; then
    printf '\nReverted by this void (re-apply if still needed):\n\n'
    sed 's/^/- /' "$TMP/collateral.txt"
  fi
} > "$MUSTFIX"

emit "$RESTORED" "$MUSTFIX"
