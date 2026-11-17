#!/bin/sh
# Joins the reviewer branches: exactly one review-* node runs, chosen by the
# route conditional. Lifts its final message into one stable output, so the
# caller reads {{collect.review}} (or childOutput.collect.review when spawned)
# without knowing which model ran.
#
# Reads the run's variables as JSON on stdin. Emits
#   {review, reviewer, node, verdict}
# verdict is parsed from the review's "VERDICT: <x>" line; "unknown" when the
# model omitted it. Fails when no review branch produced a non-empty response.
set -eu
IN="$(cat)"
printf '%s' "$IN" | jq -e -c '
  [to_entries[]
   | select((.key | startswith("review-")) and (.value | type) == "object"
            and ((.value.response // "") | type) == "string"
            and (.value.response // "") != "")] as $done
  | if ($done | length) != 1 then
      error("expected exactly one completed review-* node, found \($done | length)")
    else
      $done[0] as $r
      | ($r.value.response) as $text
      | {review: $text,
         reviewer: (.reviewer // ""),
         node: $r.key,
         verdict: (([$text | scan("(?m)^\\s*\\**VERDICT:\\**\\s*([a-z-]+)")[0]] | first) // "unknown")}
    end'
