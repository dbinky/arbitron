# Shared-install bats helpers (P2 Task 4).
#
# Layers a few shared-install conveniences on top of test_helper.bash:
#   - shared_sandbox_paths: point ARB_ETC/ARB_LIB/ARB_LOG and the systemd unit
#     path into the per-test sandbox so the real /etc and /var are never touched.
#   - shared_stage_mocks: stamp the privileged-command recording fakes
#     (sudo/useradd/getent/systemctl/install/chown/chmod/rm/userdel) plus a curl
#     that always answers 200 for the health check, and a download_verified
#     override is left to the caller (it needs $3).
#   - read_cfg/read_env/read_unit: accessors for the three generated artifacts.
#
# Source AFTER `load test_helper` and AFTER `source install.sh`.

shared_sandbox_paths() {
  export ARB_INSTALL_MODE=shared
  export ARB_ETC="$TEST_SANDBOX/etc/arbitron"
  export ARB_LIB="$TEST_SANDBOX/var/lib/arbitron"
  export ARB_LOG="$TEST_SANDBOX/var/log/arbitron"
  export ARB_SYSTEMD_UNIT_PATH="$TEST_SANDBOX/etc/systemd/system/arbitron.service"
  mkdir -p "$ARB_ETC" "$ARB_LIB" "$ARB_LOG" "$(dirname "$ARB_SYSTEMD_UNIT_PATH")"
}

# shared_stage_mocks installs the recording fakes the shared path shells out to.
# curl answers 200 (health check) and writes the binary/SHA fixtures on download.
shared_stage_mocks() {
  # Stage curl FIRST: install_fake itself runs `chmod +x`, so it must use the
  # real chmod before we shadow chmod with a no-op recording fake below.
  install_fake curl 'echo 200'
  install_mock recording-fake sudo useradd userdel getent systemctl chown chmod
  install_mock recording-install install   # records argv AND performs the copy
  install_mock recording-rm rm             # records argv AND removes in-sandbox
  hash -r   # drop cached paths to the real install/chmod/chown so the mocks win
}

read_cfg()  { cat "$ARB_ETC/config.yaml"; }
read_env()  { cat "$ARB_ETC/arbitron.env"; }
read_unit() { cat "$ARB_SYSTEMD_UNIT_PATH"; }
