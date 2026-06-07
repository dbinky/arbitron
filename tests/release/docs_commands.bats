#!/usr/bin/env bats
#
# P5 Task 3: example-command / URL self-consistency for the install docs.
#
# Asserts that the one-line install commands and version-pin examples documented
# under docs/install/*.md reference the SAME stable URLs and env-var form that
# Task 2 fixed in install.sh / install.ps1, and that the cosign trust root regex
# matches the release workflow. Best-effort shellcheck of the runnable bash
# fences (curl|bash install/upgrade snippets) when shellcheck is on PATH.

REPO_ROOT="${BATS_TEST_DIRNAME}/../.."
DOCS="${REPO_ROOT}/docs/install"

SH_URL="https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh"
PS_URL="https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1"

@test "docs/install exists with the four expected files" {
  for f in README.md upgrading.md security.md tunnels-and-tls.md; do
    [ -f "${DOCS}/${f}" ] || { echo "missing ${DOCS}/${f}"; return 1; }
  done
}

@test "the unix one-line command uses the exact stable install.sh URL" {
  grep -qF "curl -fsSL ${SH_URL} | bash" "${DOCS}/README.md"
}

@test "the windows one-line command uses the exact stable install.ps1 URL" {
  grep -qF "irm ${PS_URL} | iex" "${DOCS}/README.md"
}

@test "every documented raw script URL points at dbinky/arbitron main" {
  # No stray fork/branch URLs crept in.
  run grep -rhoE 'https://raw\.githubusercontent\.com/[^ |`]+/install\.(sh|ps1)' "${DOCS}"
  [ "$status" -eq 0 ]
  while read -r url; do
    [ -n "$url" ] || continue
    case "$url" in
      "$SH_URL"|"$PS_URL") ;;
      *) echo "unexpected script URL: $url"; return 1 ;;
    esac
  done <<< "$output"
}

@test "the same raw URLs are the ones baked into the scripts" {
  grep -qF "$SH_URL" "${REPO_ROOT}/install.sh"
  grep -qF "$PS_URL" "${REPO_ROOT}/install.ps1"
}

@test "ARBITRON_VERSION pin examples use the documented form for both shells" {
  grep -qE "ARBITRON_VERSION=[0-9]+\.[0-9]+\.[0-9]+ curl -fsSL ${SH_URL} \| bash" "${DOCS}/README.md"
  grep -qE "\\\$env:ARBITRON_VERSION='[0-9]+\.[0-9]+\.[0-9]+'; irm ${PS_URL} \| iex" "${DOCS}/README.md"
}

@test "the cosign verify command documents the dbinky/arbitron trust root" {
  grep -qF "token.actions.githubusercontent.com" "${DOCS}/security.md"
  grep -qF "dbinky/arbitron" "${DOCS}/security.md"
  grep -qF "refs/tags/v" "${DOCS}/security.md"
}

@test "installers.yml exercises make test-release in CI" {
  # tests/release/ covers artifact naming, SHA256SUMS self-consistency, version
  # round-trip, and docs URL accuracy. Without CI coverage these tests only run
  # manually; a regression can slip through on any PR that changes the Makefile,
  # Go sources, or docs without being caught.
  grep -qE 'test-release' "${REPO_ROOT}/.github/workflows/installers.yml"
}

@test "runnable bash fences in the install docs are shellcheck-clean" {
  if ! command -v shellcheck >/dev/null 2>&1; then
    skip "shellcheck not on PATH"
  fi
  # Extract every line that is a curl|bash install/upgrade snippet and lint it as
  # a standalone command. (Illustrative non-runnable fences are not extracted.)
  local tmp
  tmp="$(mktemp)"
  {
    printf '#!/usr/bin/env bash\n'
    grep -rhE '^(ARBITRON_VERSION=[^ ]+ )?curl -fsSL https://raw\.githubusercontent\.com/dbinky/arbitron/main/install\.sh \| bash$' "${DOCS}"
  } > "$tmp"
  run shellcheck -S warning "$tmp"
  rm -f "$tmp"
  [ "$status" -eq 0 ] || { echo "$output"; return 1; }
}
