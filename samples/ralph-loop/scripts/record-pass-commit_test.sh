#!/bin/sh
# Covers record-pass-commit.sh's tamper awareness (P8): a pass the tamper gate
# voided must score as no progress, but ONLY when the void belongs to THIS pass.
# A void is tied to a pass by pass_id; a stale void from an earlier pass (a reset
# restores the sha, so the next clean pass can land on the very same HEAD) must
# not poison the clean pass that follows it.
set -eu
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SUT="$SCRIPT_DIR/record-pass-commit.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1 (want $3, got $2)"; fails=$((fails+1)); fi; }

WS="$TMP/ws"; WORK="$WS/work"; mkdir -p "$WORK"
git init --quiet -b main "$WORK"
git -C "$WORK" config user.email t@t; git -C "$WORK" config user.name t
echo a > "$WORK/a"; git -C "$WORK" add a; git -C "$WORK" commit --quiet -m "chore: seed"

# fresh_pass: reset the per-run state, then commit real code so the pass has
# progress evidence on its own.
fresh_pass() {
  rm -f "$WS/last-pass-head" "$WS/last-box-counts" "$WS/passes-since-approval"
  git -C "$WORK" rev-parse HEAD > "$WS/last-pass-head"
  echo "$1" >> "$WORK/code.go"; git -C "$WORK" add code.go
  git -C "$WORK" commit --quiet -m "refine: $1"
}
run() { printf '%s' "$1" | sh "$SUT" 2>/dev/null; }
base() { jq -n --arg ws "$WS" --arg w "$WORK" '{workspace:$ws, work_dir:$w}'; }

fresh_pass one
OUT="$(run "$(base)")"
check "baseline: a code commit is progress" "$(printf '%s' "$OUT" | jq -r .progress_evidence)" "true"
check "baseline: tamper_voided false when no tamper node ran" "$(printf '%s' "$OUT" | jq -r .tamper_voided)" "false"

fresh_pass two
OUT="$(run "$(base | jq '. + {"pass-mark":{sha:"x",pass_id:7},"tamper-verdict":{voided:true,pass_id:7}}')")"
check "matching pass_id from tamper-verdict gives no progress" "$(printf '%s' "$OUT" | jq -r .progress_evidence)" "false"
check "matching pass_id from tamper-verdict: tamper_voided" "$(printf '%s' "$OUT" | jq -r .tamper_voided)" "true"
check "matching pass_id from tamper-verdict: boxes_ticked 0" "$(printf '%s' "$OUT" | jq -r .boxes_ticked)" "0"

fresh_pass three
OUT="$(run "$(base | jq '. + {"pass-mark":{sha:"x",pass_id:7},"tamper-void":{voided:true,pass_id:7}}')")"
check "the same from tamper-void: no progress" "$(printf '%s' "$OUT" | jq -r .progress_evidence)" "false"
check "the same from tamper-void: tamper_voided" "$(printf '%s' "$OUT" | jq -r .tamper_voided)" "true"

fresh_pass four
OUT="$(run "$(base | jq '. + {"pass-mark":{sha:"x",pass_id:8},"tamper-verdict":{voided:true,pass_id:7}}')")"
check "mismatched pass_id ignored: scored normally" "$(printf '%s' "$OUT" | jq -r .progress_evidence)" "true"
check "mismatched pass_id ignored: tamper_voided false" "$(printf '%s' "$OUT" | jq -r .tamper_voided)" "false"

fresh_pass five
OUT="$(run "$(base | jq '. + {"pass-mark":{sha:"x",pass_id:7},"tamper-verdict":{voided:false,pass_id:7}}')")"
check "a matching but not-voided verdict is scored normally" "$(printf '%s' "$OUT" | jq -r .progress_evidence)" "true"

# Reset-void in pass 4 restored sha S; clean pass 5 lands on the same sha S.
fresh_pass six
S="$(git -C "$WORK" rev-parse HEAD)"
OUT="$(run "$(base | jq --arg s "$S" '. + {"tamper-void":{voided:true,pass_id:4,pass_base:$s},"pass-mark":{sha:$s,pass_id:5}}')")"
check "reset-void in pass N then clean pass N+1 at the same sha: scored normally" "$(printf '%s' "$OUT" | jq -r .progress_evidence)" "true"
check "reset-void then clean pass: tamper_voided false" "$(printf '%s' "$OUT" | jq -r .tamper_voided)" "false"

# A snapshot without pass-mark.pass_id can never match.
fresh_pass seven
OUT="$(run "$(base | jq '. + {"tamper-verdict":{voided:true,pass_id:null}}')")"
check "no pass-mark: void is not trusted" "$(printf '%s' "$OUT" | jq -r .tamper_voided)" "false"

if [ "$fails" -eq 0 ]; then echo "all record-pass-commit tests passed"; else
  echo "$fails record-pass-commit test(s) FAILED"; exit 1; fi
