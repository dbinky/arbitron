#!/bin/sh
# Posts a rich informational alert to a Teams (Power Automate / Workflows) webhook.
#
# The secret webhook URL is read from the `teams_webhook_url` workflow variable
# (the stdin var snapshot) or, failing that, the ARBITRON_TEAMS_WEBHOOK_URL env
# var. It is NEVER hardcoded here, so this script is safe to commit to the repo
# sample. If no URL is configured the webhook is skipped (skipped:true) and, on
# a macOS host, a desktop notification is posted instead (local_notified; opt
# out with ARBITRON_NOTIFY_LOCAL=0). It never fails the run.
#
# Args (the engine substitutes {{vars}} before invocation):
#   $1 title    short headline
#   $2 summary  one/two-sentence markdown summary
#   $3 status   good | warning | error | info  (drives the accent color)
#   $4..$N      "Name=Value" facts rendered as a fact list / Adaptive Card FactSet;
#               the local fallback reads Stage= and Outcome= (failed|blocked|done)
set -eu

TITLE="${1:-arbitron workflow update}"
SUMMARY="${2:-}"
STATUS="${3:-info}"
shift 3 2>/dev/null || true

# clip <max> <text>: one line, at most <max> characters (an ellipsis marks a cut).
clip() {
  jq -rn --argjson m "$1" --arg s "$2" \
    '$s | gsub("[\r\n]+"; " ") | if length > $m then .[:($m - 1)] + "…" else . end'
}

# local_notify: the fallback when no webhook is configured. On a macOS host it
# posts a desktop notification (title, then "<outcome> · stage <stage>", then
# the summary, each truncated) so a failed or blocked run is never silent.
# ARBITRON_NOTIFY_LOCAL=0 opts out. Best effort: it never fails the node, and
# osascript gets 10s before it is abandoned. Prints true when it notified.
local_notify() {
  [ "${ARBITRON_NOTIFY_LOCAL:-1}" != 0 ] || { echo false; return 0; }
  [ "$(uname -s 2>/dev/null || true)" = Darwin ] || { echo false; return 0; }
  command -v osascript >/dev/null 2>&1 || { echo false; return 0; }
  outcome=""; stage=""
  for kv in "$@"; do
    case "$kv" in
      Outcome=*) outcome="${kv#Outcome=}" ;;
      Stage=*) stage="${kv#Stage=}" ;;
    esac
  done
  if [ -z "$outcome" ]; then
    case "$STATUS" in
      good|success) outcome="done" ;;
      warning|warn) outcome=blocked ;;
      error|fail)   outcome=failed ;;
      *)            outcome=info ;;
    esac
  fi
  sub="$outcome"
  [ -z "$stage" ] || sub="$sub · stage $stage"
  osascript -e 'on run argv' \
    -e 'display notification (item 1 of argv) with title (item 2 of argv) subtitle (item 3 of argv)' \
    -e 'end run' "$(clip 200 "$SUMMARY")" "$(clip 100 "$TITLE")" "$(clip 100 "$sub")" >/dev/null 2>&1 &
  pid=$!
  ( sleep 10; kill "$pid" 2>/dev/null ) >/dev/null 2>&1 &
  wd=$!
  rc=0
  wait "$pid" || rc=$?
  { kill "$wd"; wait "$wd"; } > /dev/null 2>&1 || true
  if [ "$rc" -eq 0 ]; then echo true; else echo false; fi
}

# Secret URL from the var snapshot (preferred) or env. With none, fall back to
# a local notification; the run never fails here.
IN="$(cat 2>/dev/null || true)"
URL="$(printf '%s' "$IN" | jq -r '.teams_webhook_url // empty' 2>/dev/null || true)"
[ -z "$URL" ] && URL="${ARBITRON_TEAMS_WEBHOOK_URL:-}"
if [ -z "$URL" ] || [ "$URL" = "null" ]; then
  LOCAL="$(local_notify "$@" 2>/dev/null || echo false)"
  [ "$LOCAL" = true ] || LOCAL=false
  jq -n --argjson l "$LOCAL" '{skipped:true, reason:"no teams_webhook_url configured", local_notified:$l}'
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
