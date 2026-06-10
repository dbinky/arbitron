#!/bin/sh
# Posts an in-app notification to the arbitron REST API and echoes a JSON ack.
# The message is passed as arg 1 (with {{var}} substitution done by the engine).
#
# NOTE: The arbitron REST API does not expose a create-notification endpoint;
# notifications are created internally by the engine on state transitions. The
# notifications resource lives under /api/v1/notifications (GET list,
# GET unread-count, POST mark-read, POST {id}/dismiss). This script is therefore
# best-effort: it targets the real resource path and treats a non-2xx response
# as a soft failure (notified:false) without aborting the workflow.
set -eu
MSG="${1:-}"
BASE="${ARBITRON_BASE_URL:-http://localhost:8080}"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/notifications" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg m "$MSG" '{type:"info", message:$m}')" || echo 000)"
jq -n --arg msg "$MSG" --arg code "$CODE" '{notified:($code|startswith("2")), http_code:$code, message:$msg}'
