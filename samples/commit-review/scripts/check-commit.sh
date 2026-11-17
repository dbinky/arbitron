#!/bin/sh
# Resolves the commit under review through gh, before any model is launched.
#
# Reads the run's variables as JSON on stdin (repo, commit). Emits
#   {sha, title, parents, parent_count}
# where sha is the full 40-character id the reviewer must use, so an
# abbreviated or mistyped commit fails here in seconds instead of after a
# model has spent its budget looking for it. No checkout: GitHub API only.
#
# Fails closed (non-zero exit, reason on stderr) when repo is not owner/name,
# commit is not hex, gh is missing or unauthenticated, or the commit is absent.
set -eu
IN="$(cat)"
REPO="$(printf '%s' "$IN" | jq -r '.repo // ""')"
COMMIT="$(printf '%s' "$IN" | jq -r '.commit // ""')"

if ! printf '%s' "$REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'; then
  echo "check-commit: repo must be owner/name, got '$REPO'" >&2
  exit 1
fi
if ! printf '%s' "$COMMIT" | grep -Eq '^[0-9a-fA-F]{7,40}$'; then
  echo "check-commit: commit must be a 7-40 character hex id, got '$COMMIT'" >&2
  exit 1
fi
if ! command -v gh >/dev/null 2>&1; then
  echo "check-commit: gh is not on PATH" >&2
  exit 1
fi
if ! RAW="$(gh api "repos/$REPO/commits/$COMMIT" 2>&1)"; then
  echo "check-commit: cannot read $REPO@$COMMIT through gh: $RAW" >&2
  exit 1
fi
printf '%s' "$RAW" | jq -c '{
  sha: .sha,
  title: (.commit.message | split("\n")[0]),
  parents: ([.parents[].sha] | join(" ")),
  parent_count: (.parents | length)
}'
