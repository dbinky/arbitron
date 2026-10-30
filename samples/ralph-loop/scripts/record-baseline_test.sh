#!/bin/sh
# Tests for record-baseline.sh: the measured baseline is COMMITTED to
# docs/reference/test-baseline.json before the first pass-mark (R3).
#
#   happy-path : file committed and tree clean
#   success    : lifted variables unchanged
#   edge-case  : docs/reference created when absent; unchanged file on resume
#                makes no commit
#   error      : missing baseline falls back to empty and commits nothing
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/record-baseline.sh"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }
git_q() { git -c user.email=t@t -c user.name=t -c init.defaultBranch=main "$@" >/dev/null 2>&1; }

BASE='{"suites":[{"name":"go","command":"go test ./...","paths":["."],"build_ok":true,"failed_count":2,"passed_count":9,"failing_tests":["A","B"]}]}'

mkrepo() { # <name> <with-docs-reference yes|no>
  WS="$TMP/$1"; WORK="$WS/work"
  mkdir -p "$WORK"
  git_q init "$WORK"
  echo x > "$WORK/x.txt"
  [ "$2" = yes ] && { mkdir -p "$WORK/docs/reference"; echo r > "$WORK/docs/reference/focus-areas.md"; }
  git_q -C "$WORK" add .; git_q -C "$WORK" commit -m init
  printf '%s' "$BASE" > "$WS/test-baseline.json"
}
run() { printf '{"workspace":"%s","work_dir":"%s"}' "$WS" "$WORK" | sh "$SCRIPT" 2>/dev/null; }

# ---------------------------------------------------------------- happy path
mkrepo a yes
before="$(git -C "$WORK" rev-list --count HEAD)"
out="$(run)"
check "file committed" "$(git -C "$WORK" log -1 --format=%s)" "chore(ralph): record branch-point baseline"
check "exactly one commit added" "$(( $(git -C "$WORK" rev-list --count HEAD) - before ))" "1"
check "tree clean" "$(git -C "$WORK" status --porcelain)" ""
check "committed file is the baseline" "$(git -C "$WORK" show HEAD:docs/reference/test-baseline.json | jq -c .)" "$BASE"

# ------------------------------------------------------------------- success
check "baseline_source measured" "$(printf '%s' "$out" | jq -r .baseline_source)" "measured"
check "summary counts failures" "$(printf '%s' "$out" | jq -r .baseline_summary | cut -c1-24)" "2 pre-existing failing t"

# ----------------------------------------------------------------- edge case
mkrepo b no
run >/dev/null
check "docs/reference created when absent" "$(git -C "$WORK" show HEAD:docs/reference/test-baseline.json | jq -c .)" "$BASE"
check "created-dir tree clean" "$(git -C "$WORK" status --porcelain)" ""

# Resume: the restored file is already committed and identical.
head1="$(git -C "$WORK" rev-parse HEAD)"
run >/dev/null
check "unchanged file on resume makes no commit" "$(git -C "$WORK" rev-parse HEAD)" "$head1"

# ----------------------------------------------------------------- error path
mkrepo c yes
rm "$WS/test-baseline.json"
head1="$(git -C "$WORK" rev-parse HEAD)"
out="$(run)"
check "missing baseline falls back to empty" "$(printf '%s' "$out" | jq -r .baseline_source)" "fallback-empty"
check "missing baseline commits nothing" "$(git -C "$WORK" rev-parse HEAD)" "$head1"

# A failing commit (hook rejects) names itself on stderr and fails the script.
mkrepo d yes
printf '#!/bin/sh\nexit 1\n' > "$WORK/.git/hooks/pre-commit"; chmod +x "$WORK/.git/hooks/pre-commit"
if printf '{"workspace":"%s","work_dir":"%s"}' "$WS" "$WORK" | sh "$SCRIPT" >/dev/null 2>"$TMP/d.err"; then
  check "commit failure exits non-zero" "exited 0" "non-zero"
else
  check "commit failure exits non-zero" "non-zero" "non-zero"
fi
check "commit failure prints a hint" "$(grep -c 'commit failed (git identity?)' "$TMP/d.err")" "1"

echo
if [ "$fails" -eq 0 ]; then echo "all record-baseline tests passed"; else
  echo "$fails record-baseline test(s) FAILED"; exit 1; fi
