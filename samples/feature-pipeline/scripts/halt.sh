#!/bin/sh
# Terminal failure node: prints why the pipeline stopped and exits 1 so the
# parent instance ends `failed`. Reads failed_stage / failed_reason from stdin.
STAGE=""
REASON=""
if IN="$(cat 2>/dev/null)" && [ -n "$IN" ]; then
  STAGE="$(printf '%s' "$IN" | jq -r '.failed_stage // "" | tostring' 2>/dev/null)"
  REASON="$(printf '%s' "$IN" | jq -r '.failed_reason // "" | tostring' 2>/dev/null)"
fi
if [ -n "$STAGE" ] && [ -n "$REASON" ]; then
  echo "pipeline stopped: $STAGE: $REASON" >&2
elif [ -n "$STAGE" ]; then
  echo "pipeline stopped: $STAGE" >&2
else
  echo "pipeline stopped: a pipeline step failed" >&2
fi
exit 1
