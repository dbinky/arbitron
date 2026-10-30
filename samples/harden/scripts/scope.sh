#!/bin/sh
# Resolves the diff scope for a harden run. Reads the variable snapshot as JSON
# on stdin (work_dir, resolve-inputs.inputs.base_ref, resolve-inputs.outputs.base_branch)
# and emits {base_ref, merge_base}.
#
# The base is the explicit base_ref input, else the manifest base_branch; with
# neither it exits 1. The base is fetched at depth 200 (when an origin exists)
# and the repository is unshallowed if that is what the merge-base needs.
set -eu

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
BASE="$(printf '%s' "$IN" | jq -r '."resolve-inputs".inputs.base_ref // ""')"
[ -n "$BASE" ] || BASE="$(printf '%s' "$IN" | jq -r '."resolve-inputs".outputs.base_branch // ""')"

[ -n "$WORK" ] || { echo "scope: work_dir not set" >&2; exit 1; }
[ -n "$BASE" ] || { echo "scope: no base_ref given and no manifest base_branch" >&2; exit 1; }
git -C "$WORK" rev-parse --git-dir >/dev/null 2>&1 || { echo "scope: not a git worktree: $WORK" >&2; exit 1; }

g() { git -C "$WORK" "$@"; }

if g remote get-url origin >/dev/null 2>&1; then
  g fetch -q --depth 200 origin "+refs/heads/$BASE:refs/remotes/origin/$BASE" 2>/dev/null \
    || g fetch -q --depth 200 origin "$BASE" 2>/dev/null \
    || echo "scope: could not fetch $BASE from origin; using local refs" >&2
fi

# Prefer the remote-tracking ref: the local branch may be stale or absent.
REF=""
for c in "refs/remotes/origin/$BASE" "$BASE"; do
  if g rev-parse --verify -q "$c^{commit}" >/dev/null 2>&1; then
    REF="$c"
    break
  fi
done
[ -n "$REF" ] || { echo "scope: cannot resolve base '$BASE' to a commit" >&2; exit 1; }

MB="$(g merge-base HEAD "$REF" 2>/dev/null || true)"
if [ -z "$MB" ] && [ "$(g rev-parse --is-shallow-repository 2>/dev/null)" = "true" ]; then
  echo "scope: shallow clone has no merge-base yet; unshallowing" >&2
  g fetch -q --unshallow origin 2>/dev/null || g fetch -q --unshallow 2>/dev/null \
    || echo "scope: unshallow failed" >&2
  MB="$(g merge-base HEAD "$REF" 2>/dev/null || true)"
fi
[ -n "$MB" ] || { echo "scope: no merge-base between HEAD and $BASE" >&2; exit 1; }

jq -n --arg b "$BASE" --arg m "$MB" '{base_ref:$b, merge_base:$m}'
