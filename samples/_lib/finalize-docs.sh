#!/bin/sh
# Reads the variable snapshot as JSON on stdin (work_dir). Usage:
#   finalize-docs.sh --message <msg> <path|glob> [<path|glob> ...]
# Paths and globs resolve inside work_dir. Every match must be a non-empty file;
# the matches are committed (only those paths) if they have uncommitted changes.
# Emits {paths: [numeric-aware sorted matches], committed: bool}. Never pushes.
set -eu

MSG=""
HAVE_MSG=0
PATTERNS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --message)
      [ $# -ge 2 ] || { echo "finalize-docs: --message needs a value" >&2; exit 1; }
      MSG="$2"; HAVE_MSG=1; shift ;;
    *)
      PATTERNS="$PATTERNS$1
" ;;
  esac
  shift
done
[ "$HAVE_MSG" -eq 1 ] && [ -n "$MSG" ] || { echo "finalize-docs: --message is required" >&2; exit 1; }
[ -n "$PATTERNS" ] || { echo "finalize-docs: at least one path or glob is required" >&2; exit 1; }

WORK="$(cat | jq -r '.work_dir // ""')"
[ -n "$WORK" ] || { echo "finalize-docs: work_dir not set" >&2; exit 1; }
cd "$WORK" || { echo "finalize-docs: cannot enter $WORK" >&2; exit 1; }

# Expand each pattern with the shell (globbing on, word splitting off).
FOUND=""
OLDIFS="$IFS"
IFS='
'
for p in $PATTERNS; do
  IFS="$OLDIFS"
  # shellcheck disable=SC2086
  set -- $p
  for f in "$@"; do
    if [ -f "$f" ]; then
      FOUND="$FOUND$f
"
    fi
  done
  IFS='
'
done
IFS="$OLDIFS"

FOUND="$(printf '%s' "$FOUND" | sed '/^$/d' | sort -u | sort -V)"
[ -n "$FOUND" ] || { echo "finalize-docs: no document written (nothing matched)" >&2; exit 1; }

printf '%s\n' "$FOUND" | while IFS= read -r f; do
  [ -s "$f" ] || { echo "finalize-docs: empty document: $f" >&2; exit 1; }
done || exit 1

printf '%s\n' "$FOUND" | while IFS= read -r f; do
  git add -- "$f" 1>&2
done

COMMITTED=false
STAGED="$(printf '%s\n' "$FOUND" | while IFS= read -r f; do
  git diff --cached --name-only -- "$f"
done)"
if [ -n "$STAGED" ]; then
  printf '%s\n' "$FOUND" | git commit -q -m "$MSG" --pathspec-from-file=- 1>&2
  COMMITTED=true
fi

printf '%s\n' "$FOUND" | jq -R . | jq -sc --argjson c "$COMMITTED" '{paths:., committed:$c}'
