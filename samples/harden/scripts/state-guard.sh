#!/bin/sh
# Guards the harden scoring state against the strengthen agent.
#
#   state-guard.sh --snapshot         emits {hashes: {<file>: <sha256 | "absent">}}
#   state-guard.sh --verify <node>    emits {tampered, files, restored}
#
# Reads the variable snapshot as JSON on stdin (workspace; for --verify also
# <node>.hashes, the --snapshot node's output). strengthen runs with Write
# access in {{workspace}}, and rescore keeps earlier kills while report scores
# from state.json, so an edited state.json (a mutant set to killed, an extended
# approved_equivalents) or survivors-round.json would raise the score without a
# test. The hashes live in the engine's node output, out of the agent's reach.
#
# --snapshot copies each guarded file to harden/.state-guard/. --verify compares
# the files with the recorded hashes; on any mismatch it restores every changed
# file from its copy (or removes a file that did not exist at snapshot time) and
# reports tampered:true, and the caller voids the round. A copy that no longer
# matches its recorded hash cannot be trusted, so that exits 1 (fail closed).
set -eu

FILES="state.json survivors-round.json"

die() { echo "state-guard: $*" >&2; exit 1; }

MODE="${1:-}"
case "$MODE" in
  --snapshot) [ $# -eq 1 ] || die "usage: state-guard.sh --snapshot" ;;
  --verify) [ $# -eq 2 ] && [ -n "$2" ] || die "usage: state-guard.sh --verify <snapshot node id>" ;;
  *) die "usage: state-guard.sh --snapshot | --verify <snapshot node id>" ;;
esac

IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
[ -n "$WS" ] || die "workspace not set"
H="$WS/harden"
COPY="$H/.state-guard"

# hash <file>: sha256 of the file, or "absent".
hash() {
  if [ ! -f "$1" ]; then echo absent; return 0; fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

if [ "$MODE" = --snapshot ]; then
  rm -rf "$COPY"
  mkdir -p "$COPY"
  OUT='{}'
  for f in $FILES; do
    [ ! -f "$H/$f" ] || cp "$H/$f" "$COPY/$f"
    OUT="$(printf '%s' "$OUT" | jq -c --arg f "$f" --arg h "$(hash "$H/$f")" '.[$f] = $h')"
  done
  jq -n --argjson h "$OUT" '{hashes: $h}'
  exit 0
fi

NODE="$2"
HASHES="$(printf '%s' "$IN" | jq -c --arg n "$NODE" '.[$n].hashes // null')"
[ "$HASHES" != null ] || die "no recorded hashes at $NODE.hashes"

CHANGED='[]'
for f in $FILES; do
  want="$(printf '%s' "$HASHES" | jq -r --arg f "$f" '.[$f] // ""')"
  [ -n "$want" ] || die "no recorded hash for $f"
  [ "$(hash "$H/$f")" != "$want" ] || continue
  echo "state-guard: harden/$f changed during strengthen" >&2
  CHANGED="$(printf '%s' "$CHANGED" | jq -c --arg f "$f" '. + [$f]')"
done

RESTORED=false
if [ "$CHANGED" != '[]' ]; then
  for f in $(printf '%s' "$CHANGED" | jq -r '.[]'); do
    want="$(printf '%s' "$HASHES" | jq -r --arg f "$f" '.[$f]')"
    if [ "$want" = absent ]; then
      rm -f "$H/$f"
    else
      [ "$(hash "$COPY/$f")" = "$want" ] || die "snapshot copy of $f was altered; cannot restore"
      cp "$COPY/$f" "$H/$f"
    fi
  done
  RESTORED=true
fi

jq -n --argjson c "$CHANGED" --argjson r "$RESTORED" \
  '{tampered: ($c | length > 0), files: $c, restored: $r}'
