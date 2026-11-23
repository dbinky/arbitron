#!/usr/bin/env bats
#
# Remote Runners Phase 2 Task 11: the macOS runner install scenario.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake launchctl
  # RR-07: macOS TMPDIR is under /var -> /private/var; roots built from the
  # sandbox must use the physical path or the symlink refusal would trip.
  TEST_SANDBOX="$(cd "$TEST_SANDBOX" && pwd -P)"
}

teardown() {
  install_teardown_sandbox
}

_runner_plist() { printf '%s/Library/LaunchAgents/dev.arbitron.runner.plist\n' "$HOME"; }

# curl fake serving a runner artifact + matching SHA256SUMS for version $1, os $2, arch $3.
_stage_runner_release() {
  local v="$1" os="$2" arch="$3" sum
  sum="$(printf RUNNERBIN | shasum -a 256 | cut -d' ' -f1)"
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS.sig*|*SHA256SUMS.pem*) exit 22 ;;
      *SHA256SUMS*) printf '%s  arbitron-runner_${v}_${os}_${arch}\n' '${sum}' > \"\$dest\" ;;
      *) printf RUNNERBIN > \"\$dest\" ;;
    esac
  "
}

@test "select_install_type yields Runner on macOS when ARBITRON_INSTALL_TYPE=runner" {
  ARBITRON_INSTALL_TYPE=runner run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Runner" ]
}

@test "select_install_type keeps Local on macOS for anything else" {
  ARBITRON_INSTALL_TYPE=shared run select_install_type
  [ "$output" = "Local" ]
  run select_install_type
  [ "$output" = "Local" ]
}

@test "select_install_type refuses a runner on Linux (remote runners are macOS-only)" {
  ARBITRON_UNAME_S=Linux ARBITRON_INSTALL_TYPE=runner run select_install_type
  [ "$status" -ne 0 ]
  [[ "$output" == *"macOS-only"* ]]
}

@test "parse_args --runner selects the runner install type" {
  unset ARBITRON_INSTALL_TYPE
  parse_args --runner
  [ "$ARBITRON_INSTALL_TYPE" = "runner" ]
  run select_install_type
  [ "$output" = "Runner" ]
}

@test "parse_args rejects unknown flags" {
  run parse_args --bogus
  [ "$status" -ne 0 ]
  [[ "$output" == *"unknown argument: --bogus"* ]]
}

@test "arbitron_runner_artifact_name is arbitron-runner_<v>_<os>_<arch>" {
  run arbitron_runner_artifact_name 1.2.3 darwin arm64
  [ "$output" = "arbitron-runner_1.2.3_darwin_arm64" ]
}

@test "download_runner_binary fetches the runner artifact, verifies it and installs 0755" {
  export ARBITRON_VERSION="1.2.3"
  _stage_runner_release 1.2.3 darwin arm64
  run download_runner_binary
  [ "$status" -eq 0 ]
  mock_log_has "arbitron-runner_1.2.3_darwin_arm64"
  [ -x "$(runner_bin)/arbitron-runner" ]
  [ "$(file_mode "$(runner_bin)/arbitron-runner")" = "755" ]
  [ ! -e "$HOME/.arbitron/bin/arbitron" ]
}

@test "download_runner_binary aborts on checksum mismatch and installs nothing" {
  export ARBITRON_VERSION="1.2.3"
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS.sig*|*SHA256SUMS.pem*) exit 22 ;;
      *SHA256SUMS*) printf "%s  arbitron-runner_1.2.3_darwin_arm64\n" deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef > "$dest" ;;
      *) printf TAMPERED > "$dest" ;;
    esac
  '
  run download_runner_binary
  [ "$status" -ne 0 ]
  [[ "$output" == *"verification failed"* ]]
  [ ! -e "$(runner_bin)/arbitron-runner" ]
}

@test "the server download path still fetches the server artifact" {
  export ARBITRON_VERSION="1.2.3"
  local sum
  sum="$(printf BIN | shasum -a 256 | cut -d' ' -f1)"
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS.sig*|*SHA256SUMS.pem*) exit 22 ;;
      *SHA256SUMS*) printf '%s  arbitron_1.2.3_darwin_arm64\n' '${sum}' > \"\$dest\" ;;
      *) printf BIN > \"\$dest\" ;;
    esac
  "
  run fetch_binary_to "$TEST_SANDBOX/srv/arbitron"
  [ "$status" -eq 0 ]
  mock_log_has "arbitron_1.2.3_darwin_arm64"
}
