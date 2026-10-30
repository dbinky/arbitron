#!/bin/sh
# Appends one stage entry to .arbitron/pipeline/<slug>.json on <branch> and
# pushes it. Reads the variable snapshot as JSON on stdin (work_dir, branch,
# slug, workspace, optional base_branch).
#
#   manifest-append.sh --workflow <name> [--no-skip-ci] <stage> <status> '<jq filter>'
#
# The filter is evaluated against the snapshot and must yield exactly one
# object, which becomes the entry's outputs (values stay typed).
#
# work_dir is never committed in: the manifest commit is made in a private
# worktree <workspace>/.manifest-wt, on the local branch
# arbitron-manifest/<short_id>, and pushed as HEAD:refs/heads/<branch>. work_dir
# is only fast-forwarded, and only when it is on <branch>.
#
# The private worktree shares the clone's hooks, so the manifest commit gets the
# [skip ci] marker from init-workspace's commit-msg hook. --no-skip-ci commits
# with --no-verify instead: use it when the manifest commit lands on top of a
# commit meant to trigger CI (ralph's final commit), so the branch head, which
# is the PR head, still gets a CI run.
#
# git's own stderr is kept: a fetch that fails for any reason other than a
# missing branch (network, auth, a bad remote) and a push that fails for any
# reason other than a non-fast-forward rejection exit 1 with git's message.
#
# Emits {appended, path, manifest_commit, stage, status, outputs, entry}.
set -eu

die() { echo "manifest-append: $*" >&2; exit 1; }

WORKFLOW=""
HAVE_WORKFLOW=0
NO_VERIFY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --workflow) [ $# -ge 2 ] || die "--workflow needs a value"; WORKFLOW="$2"; HAVE_WORKFLOW=1; shift 2 ;;
    --no-skip-ci) NO_VERIFY="--no-verify"; shift ;;
    --) shift; break ;;
    --*) die "unknown argument: $1" ;;
    *) break ;;
  esac
done
[ "$HAVE_WORKFLOW" -eq 1 ] && [ -n "$WORKFLOW" ] || die "--workflow <name> is required"
[ $# -eq 3 ] || die "usage: manifest-append.sh --workflow <name> <stage> <status> <jq filter>"
STAGE="$1"
STATUS="$2"
FILTER="$3"

# 1. Validate, changing nothing.
case "$STAGE" in
  ideate|spec|design|plan|implement|harden|refine|review) ;;
  *) die "unknown stage: $STAGE" ;;
esac
case "$STATUS" in
  done|blocked) ;;
  *) die "unknown status: $STATUS (want done or blocked)" ;;
esac

IN="$(cat)"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch // ""')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug // ""')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
BASE="$(printf '%s' "$IN" | jq -r '.base_branch // ""')"
[ -n "$BRANCH" ] || die "branch is empty"
[ -n "$SLUG" ] || die "slug is empty"
case "$SLUG" in
  */*|.*) die "slug must not contain '/' or start with '.': $SLUG" ;;
esac
[ -n "$WORK" ] || die "work_dir not set"
[ -n "$WS" ] || die "workspace not set"

OUTS="$(printf '%s' "$IN" | jq -c "$FILTER")" || die "filter failed: $FILTER"
printf '%s\n' "$OUTS" | jq -es 'length == 1 and (.[0] | type) == "object"' >/dev/null 2>&1 \
  || die "filter must yield exactly one object"
OUTPUTS="$(printf '%s\n' "$OUTS" | jq -c '.')"

INSTANCE_ID="$(basename "$WS")"
SHORT_ID="$(printf '%s' "$INSTANCE_ID" | cut -c1-8)"
WT="$WS/.manifest-wt"
LOCAL="arbitron-manifest/$SHORT_ID"
MPATH=".arbitron/pipeline/$SLUG.json"
REF="refs/remotes/origin/$BRANCH"

ERR="$(mktemp)"
trap 'rm -f "$ERR"' EXIT HUP INT TERM

# try_fetch: returns 0 when fetched and 2 when the branch is not on origin; any
# other failure prints git's message and exits 1. LC_ALL=C keeps git's messages
# matchable.
try_fetch() {
  if LC_ALL=C git -C "$WORK" fetch -q origin "+refs/heads/$BRANCH:$REF" 2>"$ERR"; then
    return 0
  fi
  if grep -q "couldn't find remote ref" "$ERR"; then
    return 2
  fi
  cat "$ERR" >&2
  die "fetch of $BRANCH from origin failed (network, auth or remote error; git's message is above)"
}

fetch_branch() {
  try_fetch || die "branch $BRANCH not on origin"
}

# rejected: the last push lost a race or diverged (a non-fast-forward, or the
# remote ref moved under it), not some other failure.
rejected() { grep -qE '\[rejected\]|cannot lock ref|failed to update ref' "$ERR"; }

# 2. Push the stage's own commits when work_dir is on the branch. A branch that
# is only behind origin has nothing to push, but still gets its upstream; a
# diverged one is an error.
CUR="$(git -C "$WORK" symbolic-ref -q HEAD 2>/dev/null || true)"
if [ "$CUR" = "refs/heads/$BRANCH" ]; then
  RC=0
  try_fetch || RC=$?
  if [ "$RC" = 0 ] && git -C "$WORK" merge-base --is-ancestor HEAD "$REF" 2>/dev/null; then
    git -C "$WORK" branch -q --set-upstream-to="origin/$BRANCH" "$BRANCH" >&2 \
      || echo "manifest-append: warning: could not set the upstream of $BRANCH" >&2
  elif ! LC_ALL=C git -C "$WORK" push -q -u origin "$BRANCH" 2>"$ERR"; then
    cat "$ERR" >&2
    rejected && die "local $BRANCH diverged from origin (push rejected)"
    die "push of $BRANCH failed (git's message is above)"
  fi
fi

# 3. Fetch.
fetch_branch

# 4. Private worktree on a named local branch, started from origin/<branch>.
# wt_ok: $WT is a live worktree of work_dir's repository. A .git file whose
# gitdir was pruned or belongs to another clone is stale and is recreated.
wt_ok() {
  [ -e "$WT/.git" ] || return 1
  WT_COMMON="$(git -C "$WT" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || return 1
  [ "$WT_COMMON" = "$(git -C "$WORK" rev-parse --path-format=absolute --git-common-dir)" ]
}

reset_worktree() {
  if wt_ok; then
    git -C "$WT" checkout -q -f -B "$LOCAL" "$REF" >&2 \
      || die "cannot reset $WT to origin/$BRANCH"
    git -C "$WT" clean -fdq >&2
  else
    git -C "$WORK" worktree prune >&2
    rm -rf "$WT"
    git -C "$WORK" worktree add -q -f -B "$LOCAL" "$WT" "$REF" >&2 \
      || die "cannot create worktree $WT"
  fi
}

# 5. Append (also creates the file).
append_entry() {
  COMMIT="$(git -C "$WT" rev-parse "$REF")"
  FINISHED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  mkdir -p "$WT/.arbitron/pipeline"
  if [ -f "$WT/$MPATH" ]; then
    SRC="$(cat "$WT/$MPATH")"
  else
    SRC="$(jq -nc --arg slug "$SLUG" --arg branch "$BRANCH" --arg base "$BASE" \
      '{slug:$slug} + (if $base != "" then {base_branch:$base} else {} end) + {branch:$branch, stages:[]}')"
  fi
  ENTRY="$(jq -nc --arg stage "$STAGE" --arg status "$STATUS" --argjson outputs "$OUTPUTS" \
    --arg commit "$COMMIT" --arg at "$FINISHED" --arg iid "$INSTANCE_ID" --arg wf "$WORKFLOW" \
    '{stage:$stage, status:$status, outputs:$outputs, commit:$commit, finished_at:$at, instance_id:$iid, workflow:$wf}')"
  printf '%s' "$SRC" | jq --argjson e "$ENTRY" '.stages += [$e]' > "$WT/$MPATH.tmp" \
    || die "existing manifest $MPATH is not a usable JSON object with a stages array"
  mv "$WT/$MPATH.tmp" "$WT/$MPATH"
}

# 6. Commit the manifest path only.
commit_entry() {
  git -C "$WT" add "$MPATH" >&2
  # shellcheck disable=SC2086 # NO_VERIFY is empty or one word
  git -C "$WT" commit -q $NO_VERIFY -m "chore(pipeline): record $STAGE $STATUS" -- "$MPATH" >&2 \
    || die "manifest commit failed"
}

reset_worktree
append_entry
commit_entry

# 7. Push; on rejection re-fetch, rebuild on the new tip and retry once.
push_manifest() { LC_ALL=C git -C "$WT" push -q origin "HEAD:refs/heads/$BRANCH" 2>"$ERR"; }
if ! push_manifest; then
  if ! rejected; then
    cat "$ERR" >&2
    die "push of $BRANCH failed (git's message is above)"
  fi
  echo "manifest-append: push rejected; retrying once on the new origin/$BRANCH" >&2
  fetch_branch
  reset_worktree
  append_entry
  commit_entry
  push_manifest || { cat "$ERR" >&2; die "push of $BRANCH failed twice"; }
fi

MCOMMIT="$(git -C "$WT" rev-parse HEAD)"

# 8. Fast-forward the caller's branch so later nodes see the manifest commit.
if [ "$CUR" = "refs/heads/$BRANCH" ]; then
  git -C "$WORK" fetch -q origin "+refs/heads/$BRANCH:$REF" >&2 || true
  git -C "$WORK" merge -q --ff-only "$REF" >&2 \
    || echo "manifest-append: warning: could not fast-forward work_dir; the push succeeded" >&2
fi

jq -nc --arg path "$MPATH" --arg mc "$MCOMMIT" --arg stage "$STAGE" --arg status "$STATUS" \
  --argjson outputs "$OUTPUTS" --argjson entry "$ENTRY" \
  '{appended:true, path:$path, manifest_commit:$mc, stage:$stage, status:$status, outputs:$outputs, entry:$entry}'
