#!/usr/bin/env bats
#
# Task 9: layout_samples, write_config_yaml samples block, write_shared_config
# samples block, fetch_samples_to download+verify+extract.

load test_helper

# Redirect the system paths into the sandbox (mirrors shared_config.bats).
_sandbox_system_paths() {
  export ARB_ETC="$TEST_SANDBOX/etc/arbitron"
  export ARB_LIB="$TEST_SANDBOX/var/lib/arbitron"
  export ARB_LOG="$TEST_SANDBOX/var/log/arbitron"
  mkdir -p "$ARB_ETC" "$ARB_LIB" "$ARB_LOG"
}

setup() {
  install_setup_sandbox
  # Symlink tar (and gzip) into the coreutils sandbox BEFORE install_isolate_path
  # restricts PATH; neither is in the default coreutils list in test_helper.bash.
  # GNU tar (Linux CI) runs -z through an external gzip, so without it every
  # fixture build/extract failed there with "gzip: not found". macOS bsdtar has
  # gzip built in, which is why these passed locally.
  local _tar_src _gzip_src
  _tar_src="$(command -v tar 2>/dev/null || true)"
  _gzip_src="$(command -v gzip 2>/dev/null || true)"
  install_isolate_path
  local coreutils="$TEST_SANDBOX/coreutils"
  [ -n "$_tar_src" ] && ln -sf "$_tar_src" "$coreutils/tar"
  [ -n "$_gzip_src" ] && ln -sf "$_gzip_src" "$coreutils/gzip"
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  # Non-interactive defaults for local tests.
  export ARBITRON_PORT=8080
  export ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no
  export ARBITRON_DATA_DIR="$HOME/.arbitron/data"
  export ARBITRON_CLAUDE_PATH="$HOME/.arbitron/runtime/npm/bin/claude"
}

teardown() {
  install_teardown_sandbox
}

# ---------------------------------------------------------------------------
# layout_samples — local mode
# ---------------------------------------------------------------------------

@test "layout_samples returns ~/.arbitron/samples in local mode" {
  run layout_samples
  [ "$status" -eq 0 ]
  [ "$output" = "$HOME/.arbitron/samples" ]
}

# ---------------------------------------------------------------------------
# layout_samples — shared mode
# ---------------------------------------------------------------------------

@test "layout_samples returns \$ARB_LIB/samples in shared mode" {
  _sandbox_system_paths
  export ARB_INSTALL_MODE=shared
  run layout_samples
  [ "$status" -eq 0 ]
  [ "$output" = "$ARB_LIB/samples" ]
}

# ---------------------------------------------------------------------------
# write_config_yaml — samples block (local)
# ---------------------------------------------------------------------------

@test "write_config_yaml emits samples.seed true and correct sourcedir when CFG_SEED_SAMPLES=yes" {
  run_config_qa
  write_config_yaml
  local cfg
  cfg="$(layout_config)"
  grep -qE '^samples:' "$cfg"
  grep -qE '^\s+seed: true' "$cfg"
  grep -qF "sourcedir: \"$HOME/.arbitron/samples\"" "$cfg"
}

@test "write_config_yaml emits samples.seed false when CFG_SEED_SAMPLES=no" {
  run_config_qa
  CFG_SEED_SAMPLES="no"
  write_config_yaml
  local cfg
  cfg="$(layout_config)"
  grep -qE '^\s+seed: false' "$cfg"
  ! grep -qE '^\s+seed: true' "$cfg"
}

@test "ARBITRON_INSTALL_SAMPLE_WORKFLOWS=no is honoured by run_config_qa" {
  export ARBITRON_INSTALL_SAMPLE_WORKFLOWS=no
  run_config_qa
  write_config_yaml
  local cfg
  cfg="$(layout_config)"
  grep -qE '^\s+seed: false' "$cfg"
}

@test "write_config_yaml defaults samples.seed to true when CFG_SEED_SAMPLES is unset" {
  run_config_qa
  unset CFG_SEED_SAMPLES
  write_config_yaml
  grep -qE '^\s+seed: true' "$(layout_config)"
}

# ---------------------------------------------------------------------------
# write_shared_config — samples block (shared)
# ---------------------------------------------------------------------------

_shared_qa_defaults() {
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  export ARB_INSTALL_MODE=shared
  export ARBITRON_BIND_HOST=0.0.0.0
  export ARBITRON_PORT=8080
  export ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no
  export ARBITRON_SVC_USER=arbitron
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  # Clear the local-mode data-dir override so shared tests see ARB_LIB as the
  # default data root (prompt_default DATA_DIR reads ARBITRON_DATA_DIR).
  unset ARBITRON_DATA_DIR
  install_mock recording-fake chown chmod install
  hash -r
}

@test "write_shared_config emits samples.seed true when SCFG_SEED_SAMPLES=yes" {
  _sandbox_system_paths
  _shared_qa_defaults
  export ARBITRON_INSTALL_SAMPLE_WORKFLOWS=yes
  run_shared_qa
  write_shared_config
  local cfg="$ARB_ETC/config.yaml"
  grep -qE '^samples:' "$cfg"
  grep -qE '^\s+seed: true' "$cfg"
  grep -qF "sourcedir: \"$ARB_LIB/samples\"" "$cfg"
}

@test "write_shared_config emits samples.seed false when SCFG_SEED_SAMPLES=no" {
  _sandbox_system_paths
  _shared_qa_defaults
  export ARBITRON_INSTALL_SAMPLE_WORKFLOWS=no
  run_shared_qa
  write_shared_config
  local cfg="$ARB_ETC/config.yaml"
  grep -qE '^\s+seed: false' "$cfg"
  ! grep -qE '^\s+seed: true' "$cfg"
}

@test "write_shared_config sourcedir honors SCFG_DATA_DIR override" {
  _sandbox_system_paths
  _shared_qa_defaults
  export ARBITRON_INSTALL_SAMPLE_WORKFLOWS=yes
  run_shared_qa
  # Simulate operator choosing a custom data location.
  SCFG_DATA_DIR="$TEST_SANDBOX/data/custom"
  write_shared_config
  local cfg="$ARB_ETC/config.yaml"
  grep -qF "sourcedir: \"$TEST_SANDBOX/data/custom/samples\"" "$cfg"
  # Must NOT still point at ARB_LIB.
  ! grep -qF "sourcedir: \"$ARB_LIB/samples\"" "$cfg"
}

@test "shared install defaults to prompting for samples (yes default)" {
  _sandbox_system_paths
  _shared_qa_defaults
  # No ARBITRON_INSTALL_SAMPLE_WORKFLOWS set; prompt_yes_no default is yes.
  unset ARBITRON_INSTALL_SAMPLE_WORKFLOWS
  unset ARBITRON_SAMPLE_WORKFLOWS
  run_shared_qa
  [ "$SCFG_SEED_SAMPLES" = "yes" ]
}

# ---------------------------------------------------------------------------
# fetch_samples_to — download + verify + extract
# ---------------------------------------------------------------------------

# Build a small stub tarball + matching SHA256SUMS in the given dir.
# Prints the tarball basename.
_make_samples_fixture() {
  local dir="$1" version="${2:-1.2.3}"
  local tarball="arbitron_samples_${version}.tar.gz"
  # Build a tiny tar with a workflow dir.
  local srcdir="$dir/src"
  mkdir -p "$srcdir/pr-review" "$srcdir/feature-pipeline" "$srcdir/_lib"
  printf 'shared library\n' >"$srcdir/_lib/README.md"
  printf 'pr-review workflow\n' >"$srcdir/pr-review/workflow.yaml"
  printf 'feature-pipeline workflow\n' >"$srcdir/feature-pipeline/workflow.yaml"
  tar -czf "$dir/$tarball" -C "$srcdir" .
  local hash
  hash="$(sha256_tool <"$dir/$tarball")"
  printf '%s  %s\n' "$hash" "$tarball" >"$dir/SHA256SUMS"
  printf '%s\n' "$tarball"
}

@test "fetch_samples_to extracts workflow dirs into destdir on a valid tarball" {
  export ARBITRON_VERSION="1.2.3"
  local fixture_dir="$TEST_SANDBOX/fixture"
  mkdir -p "$fixture_dir"
  local tarball
  tarball="$(_make_samples_fixture "$fixture_dir")"
  # Mock curl: serve the fixture files from the local dir.
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) cp "'"$fixture_dir"'/SHA256SUMS" "$dest" ;;
      *) cp "'"$fixture_dir"'/$tarball" "$dest" ;;
    esac
  '
  # Substitute the tarball var into the fake at write-time.
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS*) cp '$fixture_dir/SHA256SUMS' \"\$dest\" ;;
      *)            cp '$fixture_dir/$tarball'   \"\$dest\" ;;
    esac
  "
  local destdir="$TEST_SANDBOX/samples-out"
  run fetch_samples_to "$destdir"
  [ "$status" -eq 0 ]
  [ -d "$destdir/pr-review" ]
  [ -d "$destdir/feature-pipeline" ]
}

@test "fetch_samples_to extracts the _lib library folder" {
  export ARBITRON_VERSION="1.2.3"
  local fixture_dir="$TEST_SANDBOX/fixture-lib"
  mkdir -p "$fixture_dir"
  local tarball
  tarball="$(_make_samples_fixture "$fixture_dir")"
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS*) cp '$fixture_dir/SHA256SUMS' \"\$dest\" ;;
      *)            cp '$fixture_dir/$tarball'   \"\$dest\" ;;
    esac
  "
  local destdir="$TEST_SANDBOX/samples-lib-out"
  run fetch_samples_to "$destdir"
  [ "$status" -eq 0 ]
  [ -f "$destdir/_lib/README.md" ]
}

@test "fetch_samples_to fails closed on checksum mismatch and returns non-zero" {
  export ARBITRON_VERSION="1.2.3"
  local fixture_dir="$TEST_SANDBOX/fixture2"
  mkdir -p "$fixture_dir"
  local tarball
  tarball="$(_make_samples_fixture "$fixture_dir")"
  # Corrupt the SHA256SUMS (wrong hash).
  printf 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  %s\n' \
    "$tarball" >"$fixture_dir/SHA256SUMS"
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS*) cp '$fixture_dir/SHA256SUMS' \"\$dest\" ;;
      *)            cp '$fixture_dir/$tarball'   \"\$dest\" ;;
    esac
  "
  local destdir="$TEST_SANDBOX/samples-fail"
  run fetch_samples_to "$destdir"
  [ "$status" -ne 0 ]
}

@test "fetch_samples_to returns non-zero when SHA256SUMS download fails" {
  export ARBITRON_VERSION="1.2.3"
  install_fake curl 'exit 1'
  local destdir="$TEST_SANDBOX/samples-no-sums"
  run fetch_samples_to "$destdir"
  [ "$status" -ne 0 ]
}

@test "fetch_samples_to returns non-zero when tarball download fails" {
  export ARBITRON_VERSION="1.2.3"
  local fixture_dir="$TEST_SANDBOX/fixture3"
  mkdir -p "$fixture_dir"
  _make_samples_fixture "$fixture_dir" >/dev/null
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS*) cp '$fixture_dir/SHA256SUMS' \"\$dest\" ;;
      *)            exit 1 ;;
    esac
  "
  local destdir="$TEST_SANDBOX/samples-no-tarball"
  run fetch_samples_to "$destdir"
  [ "$status" -ne 0 ]
}
