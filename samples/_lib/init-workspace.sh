#!/bin/sh
# Reads the variable snapshot as JSON on stdin; clones repo_url into
# <workspace>/src, adds a worktree at <workspace>/work, installs the [skip ci]
# commit-msg hook and emits {work_dir, src_dir, branch, base_ref, head_sha,
# run_date, short_id, slug, mode} as one JSON object on stdout.
#
# Exactly one mode:
#   --checkout         work on the existing remote branch `branch`
#   --detached-base    detached worktree at base_branch (or the remote HEAD)
#   --new-branch       dev-<slug>-<run_date>-<short_id> cut from the base
#   --mode-var <key>   snapshot[key] is checkout | detached-base | new-branch
set -eu

MODE=""
MODE_VAR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --checkout|--detached-base|--new-branch)
      [ -z "$MODE" ] && [ -z "$MODE_VAR" ] || { echo "init-workspace: exactly one mode allowed" >&2; exit 1; }
      MODE="${1#--}" ;;
    --mode-var)
      [ -z "$MODE" ] && [ -z "$MODE_VAR" ] || { echo "init-workspace: exactly one mode allowed" >&2; exit 1; }
      [ $# -ge 2 ] || { echo "init-workspace: --mode-var needs a key" >&2; exit 1; }
      MODE_VAR="$2"; shift ;;
    *)
      echo "init-workspace: unknown argument: $1" >&2
      exit 1 ;;
  esac
  shift
done
[ -n "$MODE" ] || [ -n "$MODE_VAR" ] || { echo "init-workspace: a mode is required" >&2; exit 1; }

IN="$(cat)"

if [ -n "$MODE_VAR" ]; then
  MODE="$(printf '%s' "$IN" | jq -r --arg k "$MODE_VAR" '.[$k] // ""')"
  [ -n "$MODE" ] || { echo "init-workspace: mode variable '$MODE_VAR' is absent" >&2; exit 1; }
fi
case "$MODE" in
  checkout|detached-base|new-branch) ;;
  *) echo "init-workspace: unknown mode '$MODE' (checkout, detached-base or new-branch)" >&2; exit 1 ;;
esac

WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
REPO_URL="$(printf '%s' "$IN" | jq -r '.repo_url // ""')"
SLUG_IN="$(printf '%s' "$IN" | jq -r '.slug // ""')"
BASE_BRANCH_IN="$(printf '%s' "$IN" | jq -r '.base_branch // ""')"
BRANCH_IN="$(printf '%s' "$IN" | jq -r '.branch // ""')"

[ -n "$WS" ] || { echo "workspace not set" >&2; exit 1; }
[ -n "$REPO_URL" ] || { echo "repo_url not set" >&2; exit 1; }

# SECURITY: repo_url is an attacker-controllable workflow input passed to
# `git clone`. A leading "-" is parsed as an option (--upload-pack=<cmd>) and
# the ext:: transport runs commands, either of which is RCE on the engine host.
# Allow only safe transports, reject a leading dash, and pass the URL after "--".
#   https://host/...   ssh://host/...   git@host:path   file:///abs/path
case "$REPO_URL" in
  -*)
    echo "repo_url is not an allowed git URL (must not begin with '-'): $REPO_URL" >&2
    exit 1 ;;
  https://*|ssh://*|file://*)
    ;;
  git@*:*)
    ;;
  *)
    echo "repo_url is not an allowed git URL (allowed: https://, ssh://, git@host:path, file://): $REPO_URL" >&2
    exit 1 ;;
esac

# check_ref <name> <value>: reject a leading '-' (option injection) and any
# malformed ref name (spaces, "..", "~", "^", ":", control chars, ...).
check_ref() {
  case "$2" in
    -*)
      echo "$1 must not begin with '-': $2" >&2
      exit 1 ;;
  esac
  if ! git check-ref-format "refs/heads/$2" >/dev/null 2>&1; then
    echo "$1 is not a valid git branch name: $2" >&2
    exit 1
  fi
}

BASE_BRANCH=""
if [ -n "$BASE_BRANCH_IN" ]; then
  check_ref base_branch "$BASE_BRANCH_IN"
  BASE_BRANCH="$BASE_BRANCH_IN"
fi

BRANCH=""
if [ "$MODE" = checkout ]; then
  [ -n "$BRANCH_IN" ] || { echo "branch not set (required for checkout)" >&2; exit 1; }
  check_ref branch "$BRANCH_IN"
  BRANCH="$BRANCH_IN"
fi

# SECURITY: the slug becomes part of the branch name that reaches `git worktree
# add -b`, `git push` and `gh pr create`. Reduce it to lowercase [a-z0-9-],
# squeeze disallowed runs to one '-' and trim the ends; fall back to "feature".
if [ -z "$SLUG_IN" ]; then
  SLUG="feature"
else
  SLUG="$(printf '%s' "$SLUG_IN" \
    | tr '[:upper:]' '[:lower:]' \
    | tr -cs 'a-z0-9' '-' \
    | sed 's/^-*//; s/-*$//')"
  [ -n "$SLUG" ] || SLUG="feature"
fi

SRC="$WS/src"
WORK="$WS/work"
RUN_DATE="$(date +%Y-%m-%d)"

# The workspace dir is .../instances/<instance-id>; a short id prefix keeps two
# instances building the same feature on the same day from colliding on the
# shared remote.
SHORT_ID="$(basename "$WS" | cut -c1-8)"

# A previous attempt may have left a partial clone, worktree or manifest
# worktree behind; all are per-instance scratch (the real work lives on the
# remote branch), so clear them.
rm -rf "$SRC" "$WORK" "$WS/.manifest-wt"
mkdir -p "$WS"

# Shallow clone: only the tip is needed. "--branch=<ref>" binds the value to
# the option; "--" ends option parsing for the positional args.
if [ "$MODE" = checkout ]; then
  CLONE_REF="$BRANCH"
else
  CLONE_REF="$BASE_BRANCH"
fi
if [ -n "$CLONE_REF" ]; then
  if ! git clone --depth 1 --quiet "--branch=$CLONE_REF" -- "$REPO_URL" "$SRC" 1>&2; then
    echo "init-workspace: branch '$CLONE_REF' not found on origin (or clone failed): $REPO_URL" >&2
    exit 1
  fi
else
  git clone --depth 1 --quiet -- "$REPO_URL" "$SRC" 1>&2
fi

if [ -n "$BASE_BRANCH" ]; then
  BASE_REF="$BASE_BRANCH"
elif [ "$MODE" = checkout ]; then
  BASE_REF="$BRANCH"
else
  BASE_REF="$(git -C "$SRC" symbolic-ref --short HEAD)"
fi

# The clone leaves its branch checked out in $SRC and a branch cannot be
# checked out in two worktrees at once; detach it so the ref is free.
git -C "$SRC" checkout --detach --quiet 1>&2

case "$MODE" in
  checkout)
    git -C "$SRC" worktree add --quiet "$WORK" "$BRANCH" 1>&2 ;;
  detached-base)
    git -C "$SRC" worktree add --quiet --detach "$WORK" HEAD 1>&2 ;;
  new-branch)
    BRANCH="dev-$SLUG-$RUN_DATE-$SHORT_ID"
    git -C "$SRC" worktree add --quiet -b "$BRANCH" "$WORK" HEAD 1>&2 ;;
esac

# CI COST: every in-loop commit carries "[skip ci]" so per-pass pushes do not
# trigger the repository's CI each time. Applied by a commit-msg hook so agents
# cannot forget it; the one final commit uses --no-verify so CI runs once on the
# finished state. --git-path resolves the shared hooks dir of a linked worktree
# and may be relative to the worktree.
HOOK_DIR="$(git -C "$WORK" rev-parse --git-path hooks)"
case "$HOOK_DIR" in /*) ;; *) HOOK_DIR="$WORK/$HOOK_DIR" ;; esac
mkdir -p "$HOOK_DIR"
cat > "$HOOK_DIR/commit-msg" <<'HOOK'
#!/bin/sh
# Installed by arbitron @lib/init-workspace.sh: every in-loop commit is marked
# so the repository's CI skips it. The final commit is made with --no-verify.
MSG="$1"
grep -qiE '\[(skip ci|ci skip|no ci|skip actions|actions skip)\]' "$MSG" && exit 0
printf '\n[skip ci]\n' >> "$MSG"
HOOK
chmod +x "$HOOK_DIR/commit-msg"

HEAD_SHA="$(git -C "$WORK" rev-parse HEAD)"

jq -n --arg work "$WORK" --arg src "$SRC" --arg br "$BRANCH" --arg base "$BASE_REF" \
  --arg sha "$HEAD_SHA" --arg rd "$RUN_DATE" --arg sid "$SHORT_ID" --arg slug "$SLUG" --arg mode "$MODE" \
  '{work_dir:$work, src_dir:$src, branch:$br, base_ref:$base, head_sha:$sha, run_date:$rd, short_id:$sid, slug:$slug, mode:$mode}'
