#!/usr/bin/env bats
# Contract tests for the mutation-tool registry consumed by the harden adapters.

REGISTRY="${BATS_TEST_DIRNAME}/mutation-tools.json"

@test "registry lists exactly the six languages" {
  run jq -r '.tools[].language' "$REGISTRY"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | sort | tr '\n' ' ')" = "csharp go java js python rust " ]
}

@test "every entry has the required keys" {
  run jq -e '
    .version == 1 and
    ([.tools[] |
      (.tested_version == null or (.tested_version | type) == "string") and
      ([.language, .tool, .probe, .install_hint, .default_test_command,
        .test_command_injection, .report.format, .report.path,
        .diff_scoping.mode, .diff_scoping.how] | all(. != null)) and
      ((.markers | type) == "array" and (.markers | length) > 0) and
      ((.source_ext | type) == "array" and (.source_ext | length) > 0)
    ] | all)' "$REGISTRY"
  [ "$status" -eq 0 ]
}

@test "default test commands match the spec" {
  run jq -r '.tools[] | select(.language != "java") | "\(.language)=\(.default_test_command)"' "$REGISTRY"
  [ "$status" -eq 0 ]
  expected="csharp=dotnet test
go=go test ./...
js=npm test
python=pytest
rust=cargo test"
  [ "$(printf '%s\n' "$output" | sort)" = "$expected" ]
  run jq -c '.tools[] | select(.language == "java") | .default_test_command' "$REGISTRY"
  [ "$output" = '{"pom.xml":"mvn test"}' ]
}

@test "java is Maven-only: probe, markers and hint carry no Gradle" {
  [ "$(jq -r '.tools[] | select(.language == "java") | .probe' "$REGISTRY")" = "mvn -v" ]
  [ "$(jq -c '.tools[] | select(.language == "java") | .markers' "$REGISTRY")" = '["pom.xml"]' ]
  run jq -r '.tools[] | select(.language == "java") | [.probe, .install_hint, (.markers | join(" ")), (.default_test_command | tostring)] | join(" ")' "$REGISTRY"
  [[ "$output" != *[Gg]radle* ]]
}

@test "diff_scoping.mode is native or target-list" {
  run jq -e '[.tools[].diff_scoping.mode | . == "native" or . == "target-list"] | all' "$REGISTRY"
  [ "$status" -eq 0 ]
}

@test "every install hint pins the tested version" {
  run jq -r '.tools[] | select(.tested_version != null) | . as $t | select(($t.install_hint | contains($t.tested_version)) | not) | .language' "$REGISTRY"
  [ "$status" -eq 0 ]
  [ -z "$output" ] || { echo "install hint without the tested version: $output"; return 1; }
}

@test "no install hint floats on latest" {
  run jq -r '.tools[] | select(.install_hint | test("@latest")) | .language' "$REGISTRY"
  [ -z "$output" ] || { echo "floating hint: $output"; return 1; }
}

@test "PIT's pinned version is the version the adapter runs" {
  want="$(jq -r '.tools[] | select(.language == "java") | .tested_version' "$REGISTRY")"
  have="$(sed -n 's/^PIT_VERSION=//p' "${BATS_TEST_DIRNAME}/mutate-java.sh")"
  [ "$want" = "$have" ]
  [ "$(jq -r '.tools[] | select(.language == "java") | .version_source' "$REGISTRY")" = pinned ]
}
