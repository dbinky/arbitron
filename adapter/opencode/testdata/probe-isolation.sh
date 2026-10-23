#!/bin/sh
# Reproduces the D12 OpenCode probe-isolation recipe against an installed binary.
# Usage: sh adapter/opencode/testdata/probe-isolation.sh [opencode-binary]
#
# Runs in a throwaway HOME/XDG_CONFIG_HOME holding hostile inherited config: an
# MCP server in the global XDG layer, another in $HOME/.opencode, and a plugin in
# the global plugin directory. Each would touch a marker file if it started. The
# provider points at an unreachable loopback port, so no model is ever called and
# nothing is paid for; the run fails after OpenCode has initialized MCP/plugins.
# Exit 0 means every check below held.
set -u
OC=${1:-opencode}
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/home/.config/opencode" "$T/home/.opencode" "$T/cwd"
printf '{"mcp":{"hostile-global":{"type":"local","command":["/bin/sh","-c","touch %s/MARKER-global"],"enabled":true}}}\n' "$T/cwd" > "$T/home/.config/opencode/opencode.json"
printf '{"mcp":{"hostile-dotdir":{"type":"local","command":["/bin/sh","-c","touch %s/MARKER-dotdir"]}}}\n' "$T/cwd" > "$T/home/.opencode/opencode.json"
P='"provider":{"ollama":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:9/v1"},"models":{"x":{}}}}'
D='"mcp":{"hostile-global":{"enabled":false},"hostile-dotdir":{"enabled":false}}'
fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1 (got: $2, want: $3)"; fail=1; fi; }
# run CONFIG TIMEOUT ARGS... runs opencode in the sandbox with an inline config.
run() { cfg=$1; secs=$2; shift 2; (cd "$T/cwd" && HOME="$T/home" XDG_CONFIG_HOME="$T/home/.config" OPENCODE_CONFIG_CONTENT="$cfg" timeout "$secs" "$OC" "$@" >"$T/last.log" 2>&1); }
resolved() { (cd "$T/cwd" && HOME="$T/home" XDG_CONFIG_HOME="$T/home/.config" OPENCODE_CONFIG_CONTENT="$1" "$OC" debug config --pure 2>/dev/null | tr -d ' \n'); }
# count NAME... counts which of the named marker files exist (portable: no ls|grep).
count() { n=0; for m in "$@"; do [ -e "$T/cwd/MARKER-$m" ] && n=$((n + 1)); done; echo "$n"; }

echo "opencode $("$OC" --version)"
# Phase 1: MCP servers from the global XDG layer and $HOME/.opencode.
check "debug config resolves both inherited layers" "$(resolved "{$P}" | grep -o '"hostile-[a-z]*"' | sort -u | tr '\n' ' ')" '"hostile-dotdir" "hostile-global" '
check "inline overlay deep-merges enabled:false over both" "$(resolved "{$P,$D}" | grep -o '"enabled":[a-z]*' | sort -u | tr '\n' ' ')" '"enabled":false '
check "debug config starts no server" "$(count global dotdir)" "0"
run "{$P}" 120 run --format json -m ollama/x "say hi"
check "control: an unrestricted run starts both inherited MCP servers" "$(count global dotdir)" "2"
rm -f "$T"/cwd/MARKER-*
run "{$P,$D}" 120 run --pure --format json -m ollama/x "say hi"
check "D12: overlay + --pure starts no MCP server" "$(count global dotdir)" "0"

# Phase 2: a plugin in the global plugin directory. OpenCode installs the
# config directory's plugin dependencies on first use (network, slow; NOT
# prevented by --pure); allow for that before judging the control.
mkdir -p "$T/home/.config/opencode/plugin"
printf 'import { writeFileSync } from "node:fs";\nexport const Hostile = async () => { writeFileSync("%s/MARKER-plugin", "x"); return {}; };\n' "$T/cwd" > "$T/home/.config/opencode/plugin/hostile.js"
run "{$P,$D}" 600 run --format json -m ollama/x "say hi"
check "control: without --pure the inherited plugin loads" "$(count plugin)" "1"
rm -f "$T"/cwd/MARKER-*
run "{$P,$D}" 120 run --pure --format json -m ollama/x "say hi"
check "D12: --pure keeps the inherited plugin from loading" "$(count plugin global dotdir)" "0"
exit $fail
