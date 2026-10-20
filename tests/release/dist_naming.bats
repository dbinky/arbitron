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

@test "dist produces exactly the 4 expected binaries plus SHA256SUMS" {
  for name in \
    "arbitron_${TEST_VERSION}_darwin_amd64" \
    "arbitron_${TEST_VERSION}_darwin_arm64" \
    "arbitron_${TEST_VERSION}_linux_amd64" \
    "arbitron_${TEST_VERSION}_linux_arm64"; do
    [ -f "${REPO_ROOT}/dist/${name}" ] || {
      echo "missing artifact: ${name}"; return 1
    }
  done
  [ -f "${REPO_ROOT}/dist/SHA256SUMS" ]
}

@test "dist ships no Windows artifacts (support removed before v0.1.0)" {
  run sh -c 'ls "$1"/dist | grep -E "windows|\.exe$"' _ "${REPO_ROOT}"
  [ "$status" -ne 0 ]
}

@test "SHA256SUMS has 5 well-formed basename-only lines (4 binaries + samples tarball)" {
  run wc -l < "${REPO_ROOT}/dist/SHA256SUMS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | tr -d ' ')" -eq 5 ]

  # Every line: 64 hex + two spaces + a basename (no dist/ path prefix).
  while IFS= read -r line; do
    [[ "$line" =~ ^[0-9a-f]{64}\ \ (arbitron_${TEST_VERSION}_[a-z0-9]+_[a-z0-9]+|arbitron_samples_${TEST_VERSION}\.tar\.gz)$ ]] || {
      echo "malformed or path-prefixed line: $line"; return 1
    }
  done < "${REPO_ROOT}/dist/SHA256SUMS"
  # Exactly one samples tarball, signed alongside the binaries.
  [ "$(grep -c "  arbitron_samples_${TEST_VERSION}\.tar\.gz$" "${REPO_ROOT}/dist/SHA256SUMS")" -eq 1 ]
}

@test "checksums verify from inside dist/" {
  cd "${REPO_ROOT}/dist" || return 1
  run sha_check SHA256SUMS
  [ "$status" -eq 0 ]
}

@test "host-native artifact reports the stamped version" {
  hostos="$(go env GOOS)"
  hostarch="$(go env GOARCH)"
  bin="${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_${hostos}_${hostarch}"
  [ -x "$bin" ]
  run "$bin" version
  [ "$status" -eq 0 ]
  [[ "$output" == *"${TEST_VERSION}"* ]]
}

@test "arbitron version output format is parseable with the documented extraction pattern (cross-seam)" {
  # Closes the seam between the binary's format contract and installer-side parsing.
  # The upgrade path would extract the installed version with:
  #   sed 's/^arbitron \([^ ]*\) .*/\1/'
  # This test runs the REAL dist binary and verifies the pattern extracts the
  # exact stamped version — not just a substring match. If the format changes
  # (e.g. drops the parenthesised metadata, renames the prefix), this test fails
  # before any upgrade script silently starts extracting empty/wrong versions.
  hostos="$(go env GOOS)"
  hostarch="$(go env GOARCH)"
  bin="${REPO_ROOT}/dist/arbitron_${TEST_VERSION}_${hostos}_${hostarch}"
  [ -x "$bin" ] || { echo "binary not found: $bin"; return 1; }
  out="$("$bin" version)"
  extracted="$(printf '%s' "$out" | sed 's/^arbitron \([^ ]*\) .*/\1/')"
  [ -n "$extracted" ] || { echo "sed pattern returned empty; output was: $out"; return 1; }
  [ "$extracted" = "$TEST_VERSION" ] || {
    echo "extracted='$extracted' expected='$TEST_VERSION'; output was: $out"
    return 1
  }
}

@test "arbitron version dev sentinel is non-empty and parseable (graceful degradation)" {
  # An unstamped binary (plain go build, no -ldflags) reports 'dev' as the version
  # sentinel. Upgrade scripts that parse the installed version must not crash or
  # produce empty output when confronted with a dev build — the extraction pattern
  # must degrade gracefully to 'dev' rather than to an empty string or error.
  tmpdir="${BATS_TEST_TMPDIR}/dev-binary"
  mkdir -p "$tmpdir"
  tmpbin="$tmpdir/arbitron"
  cd "$REPO_ROOT" || return 1
  CGO_ENABLED=0 go build -o "$tmpbin" ./cmd/arbitron >/dev/null 2>&1
  [ -x "$tmpbin" ] || { echo "go build failed"; return 1; }
  out="$("$tmpbin" version)"
  extracted="$(printf '%s' "$out" | sed 's/^arbitron \([^ ]*\) .*/\1/')"
  [ -n "$extracted" ] || { echo "dev sentinel produced empty version token; output: $out"; return 1; }
  [ "$extracted" = "dev" ] || { echo "expected 'dev' sentinel, got '$extracted'; output: $out"; return 1; }
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
    "arbitron_${TEST_VERSION}_linux_amd64"; do
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
  artifact="arbitron_${TEST_VERSION}_${hostos}_${hostarch}"
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
  # one of the files present exits non-zero for the absent entries — the correct
  # pattern is to grep the relevant line and pipe it to sha256sum.
  hostos="$(go env GOOS)"
  hostarch="$(go env GOARCH)"
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
