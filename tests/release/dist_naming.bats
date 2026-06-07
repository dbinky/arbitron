#!/usr/bin/env bats
#
# P5 Task 1: release artifact naming, SHA256SUMS self-consistency, determinism,
# and version round-trip for `make dist`.
#
# Runs on macOS (shasum) and Linux (sha256sum) — the checksum tool is detected.

REPO_ROOT="${BATS_TEST_DIRNAME}/../.."
TEST_VERSION="0.0.0-test"

# sha_check <SHA256SUMS-file-in-cwd> : verify all checksums, tool-agnostic.
sha_check() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$1"
  else
    shasum -a 256 -c "$1"
  fi
}

setup() {
  cd "$REPO_ROOT" || return 1
  make dist VERSION="$TEST_VERSION" >/dev/null 2>&1
}

teardown() {
  cd "$REPO_ROOT" || return 1
  make clean-dist >/dev/null 2>&1 || true
}

@test "dist produces exactly the 6 expected artifacts plus SHA256SUMS" {
  for name in \
    "arbitron_${TEST_VERSION}_darwin_amd64" \
    "arbitron_${TEST_VERSION}_darwin_arm64" \
    "arbitron_${TEST_VERSION}_linux_amd64" \
    "arbitron_${TEST_VERSION}_linux_arm64" \
    "arbitron_${TEST_VERSION}_windows_amd64.exe" \
    "arbitron_${TEST_VERSION}_windows_arm64.exe"; do
    [ -f "${REPO_ROOT}/dist/${name}" ] || {
      echo "missing artifact: ${name}"; return 1
    }
  done
  [ -f "${REPO_ROOT}/dist/SHA256SUMS" ]
}

@test "only windows artifacts carry the .exe extension" {
  [ -f "${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_windows_amd64.exe" ]
  [ -f "${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_windows_arm64.exe" ]
  # No .exe on non-windows targets.
  [ ! -f "${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_linux_amd64.exe" ]
  [ ! -f "${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_darwin_arm64.exe" ]
}

@test "SHA256SUMS has 6 well-formed basename-only lines" {
  run wc -l < "${REPO_ROOT}/dist/SHA256SUMS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | tr -d ' ')" -eq 6 ]

  # Every line: 64 hex + two spaces + a basename (no dist/ path prefix).
  while IFS= read -r line; do
    [[ "$line" =~ ^[0-9a-f]{64}\ \ arbitron_${TEST_VERSION}_[a-z0-9]+_[a-z0-9]+(\.exe)?$ ]] || {
      echo "malformed or path-prefixed line: $line"; return 1
    }
  done < "${REPO_ROOT}/dist/SHA256SUMS"
}

@test "checksums verify from inside dist/" {
  cd "${REPO_ROOT}/dist" || return 1
  run sha_check SHA256SUMS
  [ "$status" -eq 0 ]
}

@test "host-native artifact reports the stamped version" {
  hostos="$(go env GOOS)"
  hostarch="$(go env GOARCH)"
  ext=""
  [ "$hostos" = "windows" ] && ext=".exe"
  bin="${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_${hostos}_${hostarch}${ext}"
  [ -x "$bin" ]
  run "$bin" version
  [ "$status" -eq 0 ]
  [[ "$output" == *"${TEST_VERSION}"* ]]
}

@test "two dist runs of the same commit yield byte-identical SHA256SUMS" {
  cp "${REPO_ROOT}/dist/SHA256SUMS" "${BATS_TEST_TMPDIR}/SHA256SUMS.first"
  cd "$REPO_ROOT" || return 1
  make dist VERSION="$TEST_VERSION" >/dev/null 2>&1
  run diff "${BATS_TEST_TMPDIR}/SHA256SUMS.first" "${REPO_ROOT}/dist/SHA256SUMS"
  [ "$status" -eq 0 ]
}

@test "make release without a prior make dist still produces all artifacts" {
  # Locks the release: dist Makefile dependency. If that dep is removed, make release
  # would try to sign an absent/stale SHA256SUMS. CI uses `make release` as the single
  # build+sign step, so this dependency must remain load-bearing.
  cd "$REPO_ROOT" || return 1
  make clean-dist >/dev/null 2>&1 || true
  make release VERSION="$TEST_VERSION" >/dev/null 2>&1
  [ -f "${REPO_ROOT}/dist/SHA256SUMS" ]
  for name in \
    "arbitron_${TEST_VERSION}_darwin_amd64" \
    "arbitron_${TEST_VERSION}_linux_amd64" \
    "arbitron_${TEST_VERSION}_windows_amd64.exe"; do
    [ -f "${REPO_ROOT}/dist/${name}" ] || { echo "missing: $name"; return 1; }
  done
}

@test "verify_download awk pattern parses the SHA256SUMS produced by make dist (cross-seam)" {
  # Closes the format-compatibility seam between release tooling and installer
  # consumption: apply the EXACT awk line from install.sh verify_download (line 296)
  #   awk -v a="$name" '$2==a || $2=="*"a {print $1; exit}'
  # to the real SHA256SUMS from make dist and confirm it extracts the correct hash.
  # If the release tool ever changes to BSD-sum format, path-prefixes, or another
  # variant, this test fails before anything ships.
  hostos="$(go env GOOS)"
  hostarch="$(go env GOARCH)"
  ext=""; [ "$hostos" = "windows" ] && ext=".exe"
  artifact="arbitron_${TEST_VERSION}_${hostos}_${hostarch}${ext}"
  sumsfile="${REPO_ROOT}/dist/SHA256SUMS"
  binfile="${REPO_ROOT}/dist/${artifact}"
  [ -f "$binfile" ]   || { echo "artifact not built: $artifact"; return 1; }
  [ -f "$sumsfile" ]  || { echo "SHA256SUMS not found"; return 1; }

  # Extract the hash using the identical awk expression from verify_download.
  extracted="$(awk -v a="$artifact" '$2==a || $2=="*"a {print $1; exit}' "$sumsfile")"
  [ -n "$extracted" ] || { echo "no hash found for $artifact in SHA256SUMS"; return 1; }
  # Must be 64 lowercase hex chars — a valid SHA-256 digest.
  [[ "$extracted" =~ ^[0-9a-f]{64}$ ]] || { echo "extracted hash malformed: $extracted"; return 1; }

  # Cross-check: the awk-extracted hash matches what the system checksum tool reports.
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$binfile" | awk '{print $1}')"
  else
    actual="$(shasum -a 256 "$binfile" | awk '{print $1}')"
  fi
  [ "$extracted" = "$actual" ] || {
    echo "hash mismatch: SHA256SUMS has $extracted but file is $actual"
    return 1
  }
}

@test "SHA256SUMS can be spot-checked for a single artifact via grep+sha256sum" {
  # Mirrors the verify-release CI job: only one artifact (linux_amd64) is downloaded
  # alongside SHA256SUMS, then verified. Running 'sha256sum -c SHA256SUMS' with only
  # one of six files present exits non-zero for the absent entries — the correct
  # pattern is to grep the relevant line and pipe it to sha256sum.
  hostos="$(go env GOOS)"
  hostarch="$(go env GOARCH)"
  [ "$hostos" = "windows" ] && skip "windows sha256sum not exercised in this bats suite"
  bin="arbitron_${TEST_VERSION}_${hostos}_${hostarch}"
  local tmpdir
  tmpdir="$(mktemp -d)"
  cp "${REPO_ROOT}/dist/${bin}" "${tmpdir}/"
  cp "${REPO_ROOT}/dist/SHA256SUMS" "${tmpdir}/"
  cd "$tmpdir" || return 1
  if command -v sha256sum >/dev/null 2>&1; then
    run bash -c "grep '${bin}\$' SHA256SUMS | sha256sum -c -"
  else
    run bash -c "grep '${bin}\$' SHA256SUMS | shasum -a 256 -c -"
  fi
  [ "$status" -eq 0 ]
  rm -rf "$tmpdir"
}
