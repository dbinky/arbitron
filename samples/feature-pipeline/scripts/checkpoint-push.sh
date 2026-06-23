#!/bin/sh
# Checkpoint push: pushes the feature branch to origin once the expensive
# interactive spec + design brainstorm and the aligned plans are committed, so
# that work survives a later crash/cancel/logout instead of living only in the
# instance worktree. Reads {work_dir, branch} from stdin JSON; emits
# {pushed, branch} as JSON. Auth (HOME/PATH/GH_TOKEN, git credential helper) is
# provided via the engine's script.envpassthrough — same as push-and-pr.sh.
#
# Fails loudly (non-zero) if the push fails: a broken remote/auth is worth
# surfacing right after the planning phase rather than discovering it only at the
# final push-and-pr step. The node's retry policy absorbs transient hiccups.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch')"

git -C "$WORK" push --quiet -u origin "$BRANCH" 1>&2
jq -n --arg b "$BRANCH" '{pushed:true, branch:$b}'
