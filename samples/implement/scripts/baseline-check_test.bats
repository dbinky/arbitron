#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/baseline-check.sh"
  WORK="$BATS_TEST_TMPDIR/work"
  mkdir -p "$WORK/.arbitron/pipeline"
}

run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}
snap() { jq -n --arg w "$WORK" --arg r "$1" '{work_dir:$w, baseline_rel:$r}'; }

@test "present" {
  echo '{"suites":[]}' > "$WORK/.arbitron/pipeline/x.baseline.json"
  run_it "$(snap .arbitron/pipeline/x.baseline.json)"
  [ "$status" -eq 0 ]
  [ "$(jq -r .present <<<"$output")" = "true" ]
}

@test "absent" {
  run_it "$(snap .arbitron/pipeline/x.baseline.json)"
  [ "$status" -eq 0 ]
  [ "$(jq -r .present <<<"$output")" = "false" ]
}

@test "path traversal exits 1" {
  run_it "$(snap ../x.json)"
  [ "$status" -eq 1 ]
}

@test "empty baseline_rel exits 1" {
  run_it "$(snap '')"
  [ "$status" -eq 1 ]
}
