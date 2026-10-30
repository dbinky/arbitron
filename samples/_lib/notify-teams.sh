#!/bin/sh
# Posts a rich informational alert to a Teams (Power Automate / Workflows) webhook.
#
# The secret webhook URL is read from the `teams_webhook_url` workflow variable
# (the stdin var snapshot) or, failing that, the ARBITRON_TEAMS_WEBHOOK_URL env
# var. It is NEVER hardcoded here, so this script is safe to commit to the repo
# sample. If no URL is configured the script is a clean no-op (skipped:true) and
# never fails the run.
#
# Args (the engine substitutes {{vars}} before invocation):
#   $1 title    short headline
#   $2 summary  one/two-sentence markdown summary
#   $3 status   good | warning | error | info  (drives the accent color)
#   $4..$N      "Name=Value" facts rendered as a fact list / Adaptive Card FactSet
set -eu

TITLE="${1:-arbitron workflow update}"
SUMMARY="${2:-}"
STATUS="${3:-info}"
shift 3 2>/dev/null || true

# Secret URL from the var snapshot (preferred) or env; skip cleanly if absent.
IN="$(cat 2>/dev/null || true)"
URL="$(printf '%s' "$IN" | jq -r '.teams_webhook_url // empty' 2>/dev/null || true)"
[ -z "$URL" ] && URL="${ARBITRON_TEAMS_WEBHOOK_URL:-}"
if [ -z "$URL" ] || [ "$URL" = "null" ]; then
  jq -n '{skipped:true, reason:"no teams_webhook_url configured"}'
  exit 0
fi

case "$STATUS" in
  good|success) COLOR="2EB67D" ;;
  warning|warn) COLOR="ECB22E" ;;
  error|fail)   COLOR="E01E5A" ;;
  *)            COLOR="2F6FED" ;;
esac

# Build the facts array + a markdown body from the "Name=Value" args.
FACTS='[]'
BODY="**$TITLE**

$SUMMARY"
for kv in "$@"; do
  name="${kv%%=*}"
  value="${kv#*=}"
  [ "$name" = "$kv" ] && value=""
  FACTS="$(printf '%s' "$FACTS" | jq --arg n "$name" --arg v "$value" '. + [{name:$n, value:$v}]')"
  BODY="$BODY
- **$name:** $value"
done

PAYLOAD="$(jq -n \
  --arg title "$TITLE" \
  --arg summary "$SUMMARY" \
  --arg status "$STATUS" \
  --arg color "$COLOR" \
  --arg text "$BODY" \
  --argjson facts "$FACTS" \
  '{
     source: "arbitron",
     title: $title,
     summary: $summary,
     status: $status,
     themeColor: $color,
     text: $text,
     facts: $facts,
     attachments: [
       {
         contentType: "application/vnd.microsoft.card.adaptive",
         content: {
           "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
           type: "AdaptiveCard",
           version: "1.4",
           body: [
             { type: "TextBlock", size: "Large", weight: "Bolder", wrap: true, text: $title },
             { type: "TextBlock", wrap: true, isSubtle: true, text: $summary },
             { type: "FactSet", facts: ($facts | map({title:.name, value:.value})) }
           ]
         }
       }
     ]
   }')"

CODE="$(printf '%s' "$PAYLOAD" | curl -s -o /dev/null -w '%{http_code}' \
  -X POST "$URL" -H 'Content-Type: application/json' --data-binary @- || echo 000)"

jq -n --arg code "$CODE" '{posted:($code|startswith("2")), http_code:$code}'
