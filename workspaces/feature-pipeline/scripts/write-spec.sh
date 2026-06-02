#!/bin/sh
# Writes the brainstormed spec into the worktree under docs/specs and commits.
# Reads vars from stdin JSON; emits {spec_path, slug} as JSON.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
RUN_DATE="$(printf '%s' "$IN" | jq -r '.run_date')"
SLUG="$(printf '%s' "$IN" | jq -r '.["brainstorm-spec"].slug // .slug')"
MD="$(printf '%s' "$IN" | jq -r '.["brainstorm-spec"].spec_markdown')"

[ -n "$MD" ] && [ "$MD" != "null" ] || { echo "spec_markdown empty" >&2; exit 1; }
mkdir -p "$WORK/docs/specs"
SPEC_PATH="docs/specs/$RUN_DATE-$SLUG-spec.md"
printf '%s\n' "$MD" > "$WORK/$SPEC_PATH"
git -C "$WORK" add "$SPEC_PATH" 1>&2
git -C "$WORK" commit --quiet -m "docs: add $SLUG product spec" 1>&2
jq -n --arg p "$SPEC_PATH" --arg slug "$SLUG" '{spec_path:$p, slug:$slug}'
