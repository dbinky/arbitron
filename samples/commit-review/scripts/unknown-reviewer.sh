#!/bin/sh
# The route conditional's default branch: an unrecognised reviewer value fails
# the run with the accepted values instead of silently picking a model.
# Keep this list in step with the conditions in the workflow manifest.
set -eu
REVIEWER="$(jq -r '.reviewer // ""')"
echo "commit-review: unknown reviewer '$REVIEWER'; use one of: gpt-sol, opus, sonnet, glm, deepseek" >&2
exit 1
