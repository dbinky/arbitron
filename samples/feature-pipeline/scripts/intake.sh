#!/bin/sh
# Validation only, no git. Reads the variable snapshot as JSON on stdin
# (repo_url, base_branch, idea_path, branch, slug, start_at) and emits
# {mode, init_mode, slug, repo_slug, base_branch}.
#
#   resume  branch given              -> init_mode checkout
#   idea    idea_path, no branch      -> init_mode new-branch
#   fresh   neither                   -> init_mode detached-base
#
# slug: an explicit slug sanitized to [a-z0-9-]; else, in idea mode, derived
# from the idea file's name; else "".
#
# base_branch: the explicit value if given; else "main" in fresh and idea mode
# and "" in resume mode (intake-check resolves it from the manifest).
set -eu

die() { echo "intake: $*" >&2; exit 1; }

IN="$(cat)"
get() { printf '%s' "$IN" | jq -r --arg k "$1" '.[$k] // "" | if type == "string" then . else error("\($k) must be a string") end' \
  || die "$1 must be a string"; }

REPO_URL="$(get repo_url)"
BASE_IN="$(get base_branch)"
IDEA="$(get idea_path)"
BRANCH="$(get branch)"
SLUG_IN="$(get slug)"
START="$(get start_at)"

case "$START" in
  ""|ideate|spec|design|plan|implement|harden|refine|review) ;;
  *) die "invalid start_at: $START" ;;
esac

if [ -n "$BRANCH" ]; then
  MODE=resume; INIT=checkout
elif [ -n "$IDEA" ]; then
  MODE=idea; INIT=new-branch
else
  MODE=fresh; INIT=detached-base
fi

case "$MODE" in
  resume)
    [ "$START" != ideate ] || die "start_at=ideate is invalid with a branch (ideate never runs on resume)" ;;
  idea)
    [ "$START" != ideate ] || die "idea_path given — ideate is skipped; omit start_at or use start_at=spec" ;;
  fresh)
    [ -z "$START" ] || [ "$START" = ideate ] || die "start_at=$START needs a branch or idea_path" ;;
esac

# repo_slug: owner/name for GitHub URLs; empty for file:// (tests).
[ -n "$REPO_URL" ] || die "repo_url not set"
case "$REPO_URL" in
  file://*) REPO_SLUG="" ;;
  https://github.com/*) REPO_SLUG="${REPO_URL#https://github.com/}" ;;
  ssh://git@github.com/*) REPO_SLUG="${REPO_URL#ssh://git@github.com/}" ;;
  git@github.com:*) REPO_SLUG="${REPO_URL#git@github.com:}" ;;
  *) die "repo_url is not a GitHub URL: $REPO_URL" ;;
esac
if [ -n "$REPO_SLUG" ]; then
  REPO_SLUG="${REPO_SLUG%/}"
  REPO_SLUG="${REPO_SLUG%.git}"
  case "$REPO_SLUG" in
    */*/*|*/|/*|""|*[!A-Za-z0-9._/-]*) die "cannot derive owner/name from repo_url: $REPO_URL" ;;
    */*) ;;
    *) die "cannot derive owner/name from repo_url: $REPO_URL" ;;
  esac
fi

# Same sanitizer as @lib/init-workspace.sh (lib scripts cannot be sourced).
sanitize() {
  S="$(printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | tr -cs 'a-z0-9' '-' \
    | sed 's/^-*//; s/-*$//')"
  [ -n "$S" ] || S="feature"
  printf '%s' "$S"
}

# An explicit slug names the manifest, the spec and design paths and the
# finalize-docs globs, so it is sanitized in every mode, not only where it
# becomes a branch name.
if [ -n "$SLUG_IN" ]; then
  SLUG="$(sanitize "$SLUG_IN")"
elif [ "$MODE" = idea ]; then
  B="$(basename "$IDEA")"
  SLUG="$(sanitize "${B%.md}")"
else
  SLUG=""
fi

BASE="$BASE_IN"
if [ -z "$BASE" ] && [ "$MODE" != resume ]; then
  BASE=main
fi

jq -n --arg mode "$MODE" --arg init "$INIT" --arg slug "$SLUG" --arg rs "$REPO_SLUG" --arg base "$BASE" \
  '{mode:$mode, init_mode:$init, slug:$slug, repo_slug:$rs, base_branch:$base}'
