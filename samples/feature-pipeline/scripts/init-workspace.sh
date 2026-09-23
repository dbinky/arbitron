#!/bin/sh
# Reads the variable snapshot as JSON on stdin; clones repo_url into
# {{workspace}}/src (optionally checking out base_branch), derives slug +
# run_date, creates the dev-<slug>-<run_date>-<instance-short-id> worktree at
# {{workspace}}/work, and emits {slug, run_date, work_dir, src_dir, branch,
# base_ref} as JSON.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace')"
REPO_URL="$(printf '%s' "$IN" | jq -r '.repo_url')"
SLUG_IN="$(printf '%s' "$IN" | jq -r '.slug // ""')"
BASE_BRANCH_IN="$(printf '%s' "$IN" | jq -r '.base_branch // ""')"

[ -n "$WS" ] && [ "$WS" != "null" ] || { echo "workspace not set" >&2; exit 1; }
[ -n "$REPO_URL" ] && [ "$REPO_URL" != "null" ] || { echo "repo_url not set" >&2; exit 1; }

# SECURITY (PR #18 HIGH, comment 3350510419): repo_url is an attacker-
# controllable workflow input passed to `git clone`. git treats a leading "-"
# as an option (e.g. --upload-pack=<cmd>) and supports the ext:: transport,
# either of which is remote code execution on the engine host. Validate the
# value against an allowlist of safe transports BEFORE it reaches git, reject a
# leading dash, and pass it after a "--" end-of-options separator at the clone.
#
# Allowed forms:
#   https://host/...        (the standard CI transport)
#   ssh://host/...          (explicit ssh)
#   git@host:path           (scp-style ssh shorthand)
#   file:///abs/path        (local transport; used by the offline test harness
#                            and local mirrors — not a network/RCE vector)
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

SRC="$WS/src"
WORK="$WS/work"
RUN_DATE="$(date +%Y-%m-%d)"

# The per-instance workspace dir is .../instances/<instance-id>, so its basename
# is the arbitron instance id (engine invariant: filesystem.instanceDir). A short
# prefix is appended to the branch name so two instances building the same repo
# and feature on the same day never collide on the shared remote — each clone is
# already isolated on disk by instance id; this extends that isolation to the
# pushed branch / PR.
INSTANCE_ID="$(basename "$WS")"
SHORT_ID="$(printf '%s' "$INSTANCE_ID" | cut -c1-8)"

# Slug is the feature identity. When the caller supplies none we use a neutral
# placeholder rather than the repo name: the repo name is not a feature, so
# naming the branch after it is both misleading and collision-prone (every
# feature in that repo would share it). The real per-feature slug is resolved
# after the spec brainstorm (see the repromote-slug node) and drives the
# docs/templates; the branch keeps this provisional, instance-unique name.
# SECURITY (PR #18 MEDIUM, comment 3350535218): a caller-supplied slug is an
# attacker-controllable workflow input that becomes part of $BRANCH, which
# crosses this script's boundary into downstream `git worktree add -b`,
# `git push`, and `gh pr create` argument contexts. Route it through the SAME
# allowlist sanitizer the derived path already used (lowercase [a-z0-9-] only,
# squeezing runs of disallowed chars to a single '-' and trimming leading/
# trailing '-') so one code path sanitizes every branch name. A slug containing
# spaces, slashes, "..", ";", or a leading "-" can no longer produce an invalid
# ref or inject git/gh options.
if [ -z "$SLUG_IN" ] || [ "$SLUG_IN" = "null" ]; then
  SLUG="feature"
else
  SLUG="$(printf '%s' "$SLUG_IN" \
    | tr '[:upper:]' '[:lower:]' \
    | tr -cs 'a-z0-9' '-' \
    | sed 's/^-*//; s/-*$//')"
  # If sanitization stripped everything (e.g. slug was all punctuation), fall
  # back to the neutral placeholder rather than an empty branch component.
  [ -n "$SLUG" ] || SLUG="feature"
fi

# base_branch (optional): the ref the feature worktree is based on. Like repo_url
# and slug it is an attacker-controllable workflow input that crosses into git
# argument contexts (`git clone --branch`, `git worktree add <commit-ish>`), so
# it is validated BEFORE it reaches git: reject a leading '-' (option injection)
# and require a well-formed git ref name via `git check-ref-format` (which
# rejects spaces, "..", "~", "^", ":", control chars, etc.). An empty value (the
# default) means "use the repo's default branch", preserving prior behaviour.
if [ -z "$BASE_BRANCH_IN" ] || [ "$BASE_BRANCH_IN" = "null" ]; then
  BASE_BRANCH=""
else
  case "$BASE_BRANCH_IN" in
    -*)
      echo "base_branch must not begin with '-': $BASE_BRANCH_IN" >&2
      exit 1 ;;
  esac
  if ! git check-ref-format "refs/heads/$BASE_BRANCH_IN" >/dev/null 2>&1; then
    echo "base_branch is not a valid git branch name: $BASE_BRANCH_IN" >&2
    exit 1
  fi
  BASE_BRANCH="$BASE_BRANCH_IN"
fi

# Shallow clone: the pipeline only needs the tip of the base branch to create
# the feature worktree, commit, push, and open a PR — it never walks deep
# history. --depth 1 keeps each per-instance clone code-sized instead of hauling
# the full repo history. (git ignores --depth for plain local-path clones, hence
# real runs clone an https/ssh URL.)
# When base_branch is set we shallow-clone THAT ref directly; "--branch=<ref>"
# binds the value to the option so it can never be parsed as a separate git
# option even for an unusual ref. Otherwise we clone the remote HEAD (the repo's
# default branch), exactly as before. `--` ends option parsing for the
# positional repo/dir args (defence in depth).
# A previous attempt may have left a partial clone or worktree behind; git
# refuses both, turning one recoverable failure into three dead retries.
rm -rf "$SRC" "$WORK"

if [ -n "$BASE_BRANCH" ]; then
  git clone --depth 1 --quiet "--branch=$BASE_BRANCH" -- "$REPO_URL" "$SRC" 1>&2
  BASE_REF="$BASE_BRANCH"
else
  git clone --depth 1 --quiet -- "$REPO_URL" "$SRC" 1>&2
  BASE_REF="$(git -C "$SRC" symbolic-ref --short HEAD)"
fi
BRANCH="dev-$SLUG-$RUN_DATE-$SHORT_ID"
git -C "$SRC" worktree add --quiet -b "$BRANCH" "$WORK" "$BASE_REF" 1>&2

# CI COST: every commit the loop makes carries "[skip ci]", so pushing after
# every pass (which is what protects the work) does not run the repository's CI
# on each intermediate commit. A ralph review once pushed ~100 commits into an
# open PR and triggered over 1,000 GitHub Actions runs in two days, 431 of them
# failing on states nobody would merge. GitHub skips push- and
# pull_request-triggered workflows when the head commit message carries the
# marker. It is applied by a commit-msg hook rather than by the agents' commit
# commands so it cannot be forgotten; push-and-pr.sh makes the final commit with
# --no-verify so CI runs exactly once, on the finished state. --git-path resolves
# the shared hooks dir of a linked worktree; it may come back relative to the
# worktree, hence the case.
HOOK_DIR="$(git -C "$WORK" rev-parse --git-path hooks)"
case "$HOOK_DIR" in /*) ;; *) HOOK_DIR="$WORK/$HOOK_DIR" ;; esac
mkdir -p "$HOOK_DIR"
cat > "$HOOK_DIR/commit-msg" <<'HOOK'
#!/bin/sh
# Installed by arbitron init-workspace.sh: every in-loop commit is marked so the
# repository's CI skips it. push-and-pr.sh bypasses this hook (--no-verify) for
# the one final commit CI should run on.
MSG="$1"
grep -qiE '\[(skip ci|ci skip|no ci|skip actions|actions skip)\]' "$MSG" && exit 0
printf '\n[skip ci]\n' >> "$MSG"
HOOK
chmod +x "$HOOK_DIR/commit-msg"

jq -n --arg slug "$SLUG" --arg rd "$RUN_DATE" --arg work "$WORK" --arg src "$SRC" --arg br "$BRANCH" --arg base "$BASE_REF" \
  '{slug:$slug, run_date:$rd, work_dir:$work, src_dir:$src, branch:$br, base_ref:$base}'
