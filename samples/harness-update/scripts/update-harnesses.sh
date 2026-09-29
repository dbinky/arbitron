#!/bin/sh
# Updates the agent harnesses arbitron drives — claude, codex, pi, opencode — to
# their latest release, each through its own updater (which knows its install
# method: claude's native installer, codex's standalone package, npm for pi and
# opencode).
#
# WHY: a stale harness fails in ways that look like model problems. codex-cli
# 0.156.1 rejected gpt-6.1-sol for ChatGPT accounts on its release day while
# 0.159.0 accepted it, and pi's model catalog only learns new models from a
# release. Keeping the harnesses current is what lets a new model be adopted by
# changing a profile, not by debugging a rejection.
#
# Every harness is attempted even when an earlier one fails; the script exits
# non-zero afterwards if any failed, so a broken update surfaces as a failed run
# instead of a quietly stale tool.
#
# Statuses per harness:
#   updated  — the updater ran and the version changed
#   current  — the updater ran and the version did not change
#   skipped  — not installed on this host
#   deferred — an npm-installed harness (pi, opencode) is running right now.
#              npm replaces those files in place, which can break a live agent
#              mid-pass (ralph loops run overnight); tomorrow's run retries.
#              claude and codex install side by side and swap a symlink, so a
#              running agent keeps its old binary and they update regardless.
#   failed   — the updater exited non-zero; its last lines are in `note`
#
# Reads (and ignores) the node's stdin JSON. Emits {results, updated, failed,
# summary} on stdout.
set -u
cat > /dev/null

TMP="${TMPDIR:-/tmp}/update-harnesses.$$"
mkdir -p "$TMP"
trap 'rm -rf "$TMP"' EXIT
RESULTS="$TMP/results"; : > "$RESULTS"
FAILED=0

version_of() { # harness -> dotted version, or empty
  "$1" --version < /dev/null 2>/dev/null | head -1 | grep -Eo '[0-9]+(\.[0-9]+)+' | head -1
}

record() { # tool status before after note
  jq -nc --arg t "$1" --arg s "$2" --arg b "$3" --arg a "$4" --arg n "$5" \
    '{tool:$t, status:$s, before:$b, after:$a, note:$n}' >> "$RESULTS"
}

update() { # harness in-use-pattern updater-args...  ("" pattern = safe while running)
  name="$1"; busy="$2"; shift 2
  if ! command -v "$name" > /dev/null 2>&1; then
    record "$name" skipped "" "" "not installed"
    return
  fi
  before="$(version_of "$name")"
  if [ -n "$busy" ] && pgrep -f "$busy" > /dev/null 2>&1; then
    record "$name" deferred "$before" "$before" "running; npm would replace it in place under a live agent"
    return
  fi
  if out="$("$name" "$@" < /dev/null 2>&1)"; then
    after="$(version_of "$name")"
    if [ "$before" = "$after" ]; then
      record "$name" current "$before" "$after" ""
    else
      record "$name" updated "$before" "$after" ""
    fi
  else
    FAILED=1
    record "$name" failed "$before" "$(version_of "$name")" "$(printf '%s' "$out" | tail -5)"
  fi
}

update claude   ""                update
update codex    ""                update
update pi       "pi-coding-agent" update --self
update opencode "opencode"        upgrade

SUMMARY="$(jq -rs 'map("\(.tool) \(.status)" + (if .status=="updated" then " \(.before)->\(.after)" else "" end)) | join(", ")' "$RESULTS")"
jq -s --arg s "$SUMMARY" \
  '{results:., updated:[.[]|select(.status=="updated")|.tool], failed:[.[]|select(.status=="failed")|.tool], summary:$s}' \
  "$RESULTS"

if [ "$FAILED" -ne 0 ]; then
  echo "update-harnesses: at least one update failed: $SUMMARY" >&2
  exit 1
fi
