#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Tools are PATH stubs. PATH holds only the stub dir plus a symlink farm of the
# utilities detect.sh needs, so no real mutation tool can leak in.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/detect.sh"
  REGISTRY="$BATS_TEST_DIRNAME/mutation-tools.json"
  T="$BATS_TEST_TMPDIR"
  WORK="$T/work"
  STUBS="$T/stubs"
  SYS="$T/sys"
  WS="$T/ws"
  mkdir -p "$WORK" "$STUBS" "$SYS" "$WS"
  # The sandbox mounts only the run's workspace and the toolchain; the fixtures
  # live beside it, so they are granted read-only here (what an operator would
  # do for a non-standard layout).
  export HARDEN_SANDBOX_RO="$T"
  for u in sh jq grep sed awk find sort tr head cat dirname basename mkdir rm cut uniq wc mktemp ls uname readlink sandbox-exec bwrap ln env; do
    p="$(command -v "$u")" && ln -sf "$p" "$SYS/$u"
  done
}

# stub <name> [output] : an executable on the stub PATH that prints output and succeeds
stub() {
  printf '#!/bin/sh\necho "%s"\n' "${2-}" > "$STUBS/$1"
  chmod +x "$STUBS/$1"
}

# write <relpath> [content]
write() {
  mkdir -p "$WORK/$(dirname "$1")"
  printf '%s\n' "${2-x}" > "$WORK/$1"
}

# cls <lang> <file>... : classify JSON for one language
cls() {
  l="$1"; shift
  printf '%s\n' "$@" | jq -Rn --arg l "$l" '{source_files_by_language: {($l): [inputs]}}'
}

# snap <classify-json> [baseline_path]
snap() {
  jq -n --arg w "$WORK" --arg ws "$WS" --argjson c "$1" --arg b "${2-}" \
    '{work_dir:$w, workspace:$ws, classify:$c, "resolve-inputs": {inputs: {base_ref:"main", baseline_path:$b}}}'
}

run_it() {
  run --separate-stderr env PATH="$STUBS:$SYS" sh -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}

lang() { printf '%s' "$output" | jq -c --arg l "$1" '.languages[] | select(.language == $l)'; }
field() { printf '%s' "$output" | jq -r "$1"; }

# --- Happy path

@test "supported and present is runnable" {
  stub gremlins "gremlins version 0.6.0"
  write pkg/a.go
  run_it "$(snap "$(cls go pkg/a.go)")"
  [ "$status" -eq 0 ]
  [ "$(field .runnable_count)" = "1" ]
  [ "$(field .skip_reason)" = "" ]
  [ "$(lang go | jq -r .status)" = "runnable" ]
  [ "$(lang go | jq -r .tool)" = "gremlins" ]
  [ "$(lang go | jq -r .version)" = "0.6.0" ]
  [ "$(lang go | jq -c .targets)" = '["pkg/a.go"]' ]
  [ "$(lang go | jq -r .reason)" = "" ]
}

@test "a probe that prints no version still counts as present" {
  stub cargo ""
  write src/lib.rs
  run_it "$(snap "$(cls rust src/lib.rs)")"
  [ "$status" -eq 0 ]
  [ "$(lang rust | jq -r .status)" = "runnable" ]
  [ "$(lang rust | jq -r .version)" = "" ]
}

# --- Edge cases

@test "no registry entry is skipped with a reason" {
  write x/a.cob
  run_it "$(snap "$(cls cobol x/a.cob)")"
  [ "$status" -eq 0 ]
  [ "$(lang cobol | jq -r .status)" = "skipped" ]
  [ "$(lang cobol | jq -r .reason)" = "no mutation tool registered for cobol" ]
  [ "$(field .runnable_count)" = "0" ]
  [ "$(field .skip_reason)" = "no mutation tool registered for cobol" ]
}

@test "skip_reason joins the per-language reasons" {
  write a.cob
  write b.zig
  run_it "$(snap '{"source_files_by_language":{"cobol":["a.cob"],"zig":["b.zig"]}}')"
  [ "$status" -eq 0 ]
  [ "$(field .skip_reason)" = "no mutation tool registered for cobol; no mutation tool registered for zig" ]
}

@test "a skipped language does not stop a runnable one" {
  stub gremlins "gremlins version 0.6.0"
  write a.go
  write b.cob
  run_it "$(snap '{"source_files_by_language":{"go":["a.go"],"cobol":["b.cob"]}}')"
  [ "$status" -eq 0 ]
  [ "$(field .runnable_count)" = "1" ]
  [ "$(field .skip_reason)" = "" ]
  [ "$(field '.languages | length')" = "2" ]
}

@test "a changed file that no longer exists is not a target" {
  stub gremlins "gremlins version 0.6.0"
  write keep.go
  run_it "$(snap "$(cls go keep.go gone.go)")"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -c .targets)" = '["keep.go"]' ]
}

@test "mutmut is accepted from PATH without pipx" {
  stub mutmut ""
  write app/m.py
  run_it "$(snap "$(cls python app/m.py)")"
  [ "$status" -eq 0 ]
  [ "$(lang python | jq -r .status)" = "runnable" ]
}

@test "mutmut is accepted as python3 -m mutmut" {
  printf '#!/bin/sh\n[ "$1 $2" = "-m mutmut" ] && { echo "mutmut 3.8.0"; exit 0; }\nexit 1\n' > "$STUBS/python3"
  chmod +x "$STUBS/python3"
  write app/m.py
  run_it "$(snap "$(cls python app/m.py)")"
  [ "$status" -eq 0 ]
  [ "$(lang python | jq -r .status)" = "runnable" ]
}

@test "mutmut is accepted from pipx list" {
  printf '#!/bin/sh\n[ "$1" = list ] && echo "mutmut 3.8.0"\nexit 0\n' > "$STUBS/pipx"
  chmod +x "$STUBS/pipx"
  write app/m.py
  run_it "$(snap "$(cls python app/m.py)")"
  [ "$status" -eq 0 ]
  [ "$(lang python | jq -r .version)" = "3.8.0" ]
}

@test "StrykerJS is accepted as a global stryker" {
  stub stryker "10.1.1"
  write web/a.ts
  run_it "$(snap "$(cls js web/a.ts)")"
  [ "$status" -eq 0 ]
  [ "$(lang js | jq -r .status)" = "runnable" ]
}

@test "StrykerJS is accepted as a project dependency via npx --no-install" {
  printf '#!/bin/sh\n[ "$1 $2 $3" = "--no-install stryker --version" ] && { echo "10.1.1"; exit 0; }\nexit 1\n' > "$STUBS/npx"
  chmod +x "$STUBS/npx"
  write web/a.ts
  run_it "$(snap "$(cls js web/a.ts)")"
  [ "$status" -eq 0 ]
  [ "$(lang js | jq -r .status)" = "runnable" ]
}

@test "StrykerJS: the probe runs in the changed files' package, so its own dependency is found" {
  # npx resolves --no-install against the cwd's node_modules: succeed only there.
  printf '#!/bin/sh\ngrep -q own-stryker package.json 2>/dev/null && [ "$1 $2 $3" = "--no-install stryker --version" ] && { echo "10.1.1"; exit 0; }\nexit 1\n' > "$STUBS/npx"
  chmod +x "$STUBS/npx"
  write web/package.json '{"devDependencies":{"own-stryker":"1"}}'
  write web/src/a.ts
  run_it "$(snap "$(cls js web/src/a.ts)")"
  [ "$status" -eq 0 ]
  [ "$(lang js | jq -r .status)" = "runnable" ]
}

@test "StrykerJS absent exits 1 with its install hint and never downloads" {
  printf '#!/bin/sh\ncase "$*" in *--yes*) echo downloaded; exit 0;; esac\nexit 1\n' > "$STUBS/npx"
  chmod +x "$STUBS/npx"
  write web/a.ts
  run_it "$(snap "$(cls js web/a.ts)")"
  [ "$status" -eq 1 ]
  hint="$(jq -r '.tools[] | select(.language=="js") | .install_hint' "$REGISTRY")"
  [[ "$stderr" == *"$hint"* ]]
}

# --- Error cases

@test "probe failure exits 1 listing every hint" {
  write a.go
  write src/lib.rs
  run_it "$(snap '{"source_files_by_language":{"go":["a.go"],"rust":["src/lib.rs"]}}')"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
  go_hint="$(jq -r '.tools[] | select(.language=="go") | .install_hint' "$REGISTRY")"
  rust_hint="$(jq -r '.tools[] | select(.language=="rust") | .install_hint' "$REGISTRY")"
  [[ "$stderr" == *"$go_hint"* ]]
  [[ "$stderr" == *"$rust_hint"* ]]
}

@test "a missing tool for an unchanged language is not an error" {
  stub gremlins "gremlins version 0.6.0"
  write a.go
  run_it "$(snap "$(cls go a.go)")"
  [ "$status" -eq 0 ]
}

@test "a missing work_dir exits 1" {
  run_it '{"classify":{}}'
  [ "$status" -eq 1 ]
}

# --- C# targeting

csproj() { # csproj <path> <test|lib> [reference-includes...]
  p="$1"; kind="$2"; shift 2
  mkdir -p "$WORK/$(dirname "$p")"
  {
    echo '<Project Sdk="Microsoft.NET.Sdk">'
    [ "$kind" = test ] && echo '<ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.0.0" /></ItemGroup>'
    for r in "$@"; do echo "<ItemGroup><ProjectReference Include=\"$r\" /></ItemGroup>"; done
    echo '</Project>'
  } > "$WORK/$p"
}

@test "csharp: one test project" {
  stub dotnet-stryker "5.0.0"
  printf '#!/bin/sh\necho "dotnet-stryker 5.0.0"\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj tests/Core.Tests/Core.Tests.csproj test '..\..\src\Core\Core.csproj'
  write src/Core/Thing.cs
  run_it "$(snap "$(cls csharp src/Core/Thing.cs)")"
  [ "$status" -eq 0 ]
  [ "$(lang csharp | jq -r .status)" = "runnable" ]
  [ "$(lang csharp | jq -c .targets)" = '["tests/Core.Tests/Core.Tests.csproj"]' ]
}

@test "csharp: a listed global tool that does not run fails the probe" {
  # dotnet tool list names it, but neither command the adapter runs works.
  printf '#!/bin/sh\n[ "$1 $2" = "tool list" ] && { echo "dotnet-stryker 5.0.0 dotnet-stryker"; exit 0; }\nexit 1\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj tests/Core.Tests/Core.Tests.csproj test '../../src/Core/Core.csproj'
  write src/Core/Thing.cs
  run_it "$(snap "$(cls csharp src/Core/Thing.cs)")"
  [ "$status" -eq 1 ]
  hint="$(jq -r '.tools[] | select(.language=="csharp") | .install_hint' "$REGISTRY")"
  [[ "$stderr" == *"$hint"* ]]
}

@test "csharp: dotnet stryker as a local tool is accepted" {
  # Stryker.NET's --version is a dashboard option that takes a value, so the
  # probe runs --help (the same binary the adapter runs) and reads the version
  # from the tool listing.
  printf '#!/bin/sh\ncase "$1 $2" in "stryker --help") exit 0 ;; "tool list") echo "dotnet-stryker 5.0.0 dotnet-stryker"; exit 0 ;; esac\nexit 1\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj tests/Core.Tests/Core.Tests.csproj test '../../src/Core/Core.csproj'
  write src/Core/Thing.cs
  run_it "$(snap "$(cls csharp src/Core/Thing.cs)")"
  [ "$status" -eq 0 ]
  [ "$(lang csharp | jq -r .status)" = "runnable" ]
  [ "$(lang csharp | jq -r .version)" = "5.0.0" ]
}

@test "csharp: two test projects" {
  printf '#!/bin/sh\necho "dotnet-stryker 5.0.0"\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj src/Other/Other.csproj lib
  csproj tests/A.Tests/A.Tests.csproj test '../../src/Core/Core.csproj'
  csproj tests/B.Tests/B.Tests.csproj test '../../src/Core/Core.csproj' '../../src/Other/Other.csproj'
  csproj tests/C.Tests/C.Tests.csproj test '../../src/Other/Other.csproj'
  write src/Core/Thing.cs
  run_it "$(snap "$(cls csharp src/Core/Thing.cs)")"
  [ "$status" -eq 0 ]
  [ "$(lang csharp | jq -c '.targets | sort')" = '["tests/A.Tests/A.Tests.csproj","tests/B.Tests/B.Tests.csproj"]' ]
}

@test "csharp: no referencing test project is skipped" {
  printf '#!/bin/sh\necho "dotnet-stryker 5.0.0"\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj src/App/App.csproj lib '../Core/Core.csproj'
  csproj tests/Other.Tests/Other.Tests.csproj test '../../src/App/App.csproj'
  write src/Core/Thing.cs
  run_it "$(snap "$(cls csharp src/Core/Thing.cs)")"
  [ "$status" -eq 0 ]
  [ "$(lang csharp | jq -r .status)" = "skipped" ]
  [ "$(lang csharp | jq -r .reason)" = "no test project references changed code" ]
  [ "$(field .runnable_count)" = "0" ]
}

# --- Boundary

@test "no changed source gives runnable_count 0 and skip_reason" {
  run_it "$(snap '{"source_files_by_language":{}}')"
  [ "$status" -eq 0 ]
  [ "$(field .runnable_count)" = "0" ]
  [ "$(field .skip_reason)" = "no changed source files" ]
  [ "$(field '.languages | length')" = "0" ]
}

@test "a classify without source_files_by_language is the same boundary" {
  run_it "$(snap '{}')"
  [ "$status" -eq 0 ]
  [ "$(field .skip_reason)" = "no changed source files" ]
}

# --- Test command (R6)

baseline() { # baseline <suites-json> : writes ./work/.arbitron/pipeline/x.baseline.json
  write .arbitron/pipeline/x.baseline.json '{}'
  printf '{"suites":%s}\n' "$1" > "$WORK/.arbitron/pipeline/x.baseline.json"
}

@test "baseline suite command used with test_command_source baseline" {
  stub gremlins "gremlins version 0.6.0"
  write pkg/a.go
  baseline '[{"name":"go","command":"go test -race ./pkg/...","paths":["pkg"]}]'
  run_it "$(snap "$(cls go pkg/a.go)" .arbitron/pipeline/x.baseline.json)"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .test_command)" = "go test -race ./pkg/..." ]
  [ "$(lang go | jq -r .test_command_source)" = "baseline" ]
}

@test "the suite whose paths cover the changed files wins over an earlier one" {
  stub gremlins "gremlins version 0.6.0"
  write svc/b/a.go
  baseline '[{"name":"a","command":"go test ./svc/a/...","paths":["svc/a"]},{"name":"b","command":"go test ./svc/b/...","paths":["svc/b/"]}]'
  run_it "$(snap "$(cls go svc/b/a.go)" .arbitron/pipeline/x.baseline.json)"
  [ "$(lang go | jq -r .test_command)" = "go test ./svc/b/..." ]
}

@test "no covering suite falls back to the first suite of that language" {
  stub gremlins "gremlins version 0.6.0"
  write other/a.go
  baseline '[{"name":"js","command":"npm test","paths":["web"]},{"name":"go","command":"go test ./cmd/...","paths":["cmd"]}]'
  run_it "$(snap "$(cls go other/a.go)" .arbitron/pipeline/x.baseline.json)"
  [ "$(lang go | jq -r .test_command)" = "go test ./cmd/..." ]
  [ "$(lang go | jq -r .test_command_source)" = "baseline" ]
}

@test "no baseline uses registry default per language" {
  stub gremlins "v 0.6.0"
  stub cargo "cargo-mutants 26.0.0"
  stub stryker "10.1.1"
  stub mutmut ""
  stub mvn "Apache Maven 3.9.9"
  stub gradle "Gradle 8.5"
  printf '#!/bin/sh\necho "dotnet-stryker 5.0.0"\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj tests/Core.Tests/Core.Tests.csproj test '../../src/Core/Core.csproj'
  write src/Core/Thing.cs
  write a.go
  write w/a.ts
  write p/a.py
  write src/lib.rs
  write j1/pom.xml
  write j1/src/main/java/A.java
  write j2/build.gradle
  write j2/src/main/java/B.java
  run_it "$(snap '{"source_files_by_language":{"go":["a.go"],"csharp":["src/Core/Thing.cs"],"js":["w/a.ts"],"python":["p/a.py"],"rust":["src/lib.rs"]}}')"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .test_command)" = "go test ./..." ]
  [ "$(lang csharp | jq -r .test_command)" = "dotnet test" ]
  [ "$(lang js | jq -r .test_command)" = "npm test" ]
  [ "$(lang python | jq -r .test_command)" = "pytest" ]
  [ "$(lang rust | jq -r .test_command)" = "cargo test" ]
  [ "$(field '[.languages[] | .test_command_source] | unique | join(",")')" = "registry-default" ]
}

@test "java default is mvn test with pom.xml" {
  stub mvn "Apache Maven 3.9.9"
  write j1/pom.xml
  write j1/src/main/java/A.java
  run_it "$(snap "$(cls java j1/src/main/java/A.java)")"
  [ "$status" -eq 0 ]
  [ "$(lang java | jq -r .test_command)" = "mvn test" ]
  [ "$(lang java | jq -r .test_command_source)" = "registry-default" ]
}

@test "gradle-only java is skipped, not failed" {
  # No mvn and no gradle: the skip comes before the probe.
  write j2/build.gradle
  write j2/src/main/java/B.java
  write j3/build.gradle.kts
  write j3/src/main/java/C.java
  run_it "$(snap "$(cls java j2/src/main/java/B.java j3/src/main/java/C.java)")"
  [ "$status" -eq 0 ]
  [ "$(lang java | jq -r .status)" = "skipped" ]
  [ "$(lang java | jq -r .reason)" = "PIT adapter supports Maven projects only; Gradle not supported" ]
  [ "$(lang java | jq -r .tool)" = "PIT" ]
  [ "$(lang java | jq -c .targets)" = "[]" ]
  [ "$(field .runnable_count)" = "0" ]
  [ "$(field .skip_reason)" = "PIT adapter supports Maven projects only; Gradle not supported" ]
}

@test "java targets without a pom are dropped from a Maven run" {
  stub mvn "Apache Maven 3.9.9"
  write j1/pom.xml
  write j1/src/main/java/A.java
  write j2/build.gradle
  write j2/src/main/java/B.java
  run_it "$(snap "$(cls java j1/src/main/java/A.java j2/src/main/java/B.java)")"
  [ "$status" -eq 0 ]
  [ "$(lang java | jq -r .status)" = "runnable" ]
  [ "$(lang java | jq -c .targets)" = '["j1/src/main/java/A.java"]' ]
  [ "$(lang java | jq -r .test_command)" = "mvn test" ]
}

@test "baseline without that language uses default" {
  stub mutmut ""
  write p/a.py
  baseline '[{"name":"go","command":"go test ./...","paths":["."]}]'
  run_it "$(snap "$(cls python p/a.py)" .arbitron/pipeline/x.baseline.json)"
  [ "$status" -eq 0 ]
  [ "$(lang python | jq -r .test_command)" = "pytest" ]
  [ "$(lang python | jq -r .test_command_source)" = "registry-default" ]
}

@test "a baseline_path that does not exist uses the default" {
  stub gremlins "v 0.6.0"
  write a.go
  run_it "$(snap "$(cls go a.go)" .arbitron/pipeline/missing.json)"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .test_command_source)" = "registry-default" ]
}

@test "a malformed baseline uses the default" {
  stub gremlins "v 0.6.0"
  write a.go
  write .arbitron/pipeline/x.baseline.json 'not json'
  run_it "$(snap "$(cls go a.go)" .arbitron/pipeline/x.baseline.json)"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .test_command_source)" = "registry-default" ]
}

@test "a malformed baseline warns on stderr before falling back" {
  stub gremlins "v 0.6.0"
  write a.go
  write .arbitron/pipeline/x.baseline.json 'not json'
  run_it "$(snap "$(cls go a.go)" .arbitron/pipeline/x.baseline.json)"
  [ "$status" -eq 0 ]
  [[ "$stderr" == *"baseline"*"not valid"* ]]
}

# --- Tool versions are checked against the registry's tested_version

go_case() { stub gremlins "$1"; write pkg/a.go; run_it "$(snap "$(cls go pkg/a.go)")"; }

@test "the tested version is supported, with no warning" {
  go_case "gremlins version 0.6.0"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .version_status)" = supported ]
  [ "$(lang go | jq -r .version_warning)" = "" ]
  [ "$(field '.warnings')" = "[]" ]
  [[ "$stderr" != *WARNING* ]]
}

@test "a newer patch of a 0.x tool is supported; a newer minor is not" {
  go_case "gremlins version 0.6.3"
  [ "$(lang go | jq -r .version_status)" = supported ]
  go_case "gremlins version 0.7.0"
  [ "$(lang go | jq -r .version_status)" = unsupported ]
}

@test "an unsupported version stays runnable but is reported loudly" {
  go_case "gremlins 9999.0.0"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .status)" = runnable ]
  [ "$(lang go | jq -r .version)" = "9999.0.0" ]
  [ "$(lang go | jq -r .version_status)" = unsupported ]
  [[ "$(lang go | jq -r .version_warning)" == *"gremlins 9999.0.0"*"tested 0.6.0"* ]]
  [[ "$stderr" == *"WARNING"*"9999.0.0"* ]]
  [ "$(field '.warnings | length')" = "1" ]
}

@test "a version older than the tested one is unsupported" {
  go_case "gremlins version 0.5.9"
  [ "$(lang go | jq -r .version_status)" = unsupported ]
}

@test "a probe whose output has no version is reported as unparseable" {
  go_case "gremlins dev-build"
  [ "$status" -eq 0 ]
  [ "$(lang go | jq -r .version_status)" = unparseable ]
  [[ "$stderr" == *"WARNING"*"could not be read"* ]]
  [ "$(field '.warnings | length')" = "1" ]
}

@test "StrykerJS: a newer minor of the tested major is supported, a newer major is not" {
  stub stryker "10.4.0"
  write web/a.ts
  run_it "$(snap "$(cls js web/a.ts)")"
  [ "$(lang js | jq -r .version_status)" = supported ]
  stub stryker "11.0.0"
  run_it "$(snap "$(cls js web/a.ts)")"
  [ "$(lang js | jq -r .version_status)" = unsupported ]
}

@test "StrykerJS: the project-local tool is the one checked" {
  # Only the package's own dependency resolves, and it is the wrong version.
  printf '#!/bin/sh\ngrep -q own-stryker package.json 2>/dev/null && [ "$1 $2 $3" = "--no-install stryker --version" ] && { echo "9.0.0"; exit 0; }\nexit 1\n' > "$STUBS/npx"
  chmod +x "$STUBS/npx"
  stub stryker "10.1.1"
  write web/package.json '{"devDependencies":{"own-stryker":"1"}}'
  write web/src/a.ts
  run_it "$(snap "$(cls js web/src/a.ts)")"
  [ "$status" -eq 0 ]
  [ "$(lang js | jq -r .version)" = "9.0.0" ]
  [ "$(lang js | jq -r .version_status)" = unsupported ]
}

@test "csharp: a local dotnet tool of the wrong version is reported" {
  printf '#!/bin/sh\ncase "$1 $2" in "stryker --help") exit 0 ;; "tool list") echo "dotnet-stryker 4.2.0 dotnet-stryker"; exit 0 ;; esac\nexit 1\n' > "$STUBS/dotnet"
  chmod +x "$STUBS/dotnet"
  csproj src/Core/Core.csproj lib
  csproj tests/Core.Tests/Core.Tests.csproj test '../../src/Core/Core.csproj'
  write src/Core/Thing.cs
  run_it "$(snap "$(cls csharp src/Core/Thing.cs)")"
  [ "$status" -eq 0 ]
  [ "$(lang csharp | jq -r .version)" = "4.2.0" ]
  [ "$(lang csharp | jq -r .version_status)" = unsupported ]
}

@test "PIT: Maven's version is never reported as PIT's" {
  printf '#!/bin/sh\necho "Apache Maven 3.9.6"\n' > "$STUBS/mvn"
  chmod +x "$STUBS/mvn"
  write pom.xml '<project/>'
  write src/main/java/A.java
  run_it "$(snap "$(cls java src/main/java/A.java)")"
  [ "$status" -eq 0 ]
  [ "$(lang java | jq -r .tool)" = PIT ]
  [ "$(lang java | jq -r .version)" = "1.30.1" ]
  [ "$(lang java | jq -r .runner_version)" = "3.9.6" ]
  [ "$(lang java | jq -r .version_status)" = supported ]
  [[ "$stderr" != *WARNING* ]]
}

# --- Isolation: probes run repository-controlled programs ----------------------------

@test "the probe runs sandboxed: no credential or host HOME, and nothing outside the run is writable" {
  DUMMY=dummy-token-3c44
  export GH_TOKEN="$DUMMY" ANTHROPIC_API_KEY="$DUMMY" CLAUDE_CONFIG_DIR="$T/fakehome/.claude"
  mkdir -p "$T/fakehome/.ssh" "$T/siblings"
  echo ssh-secret > "$T/fakehome/.ssh/id"
  export HOME="$T/fakehome"
  export HARDEN_SANDBOX_RO="$WORK"
  # A project-local tool, as a malicious dependency would ship it. It reports what
  # it could do through its version string: 10.<leaked tokens>.<writes that worked>.<secrets read>.
  mkdir -p "$WORK/web/node_modules/.bin"
  cat > "$WORK/web/node_modules/.bin/stryker" <<EOS
#!/bin/sh
leak=\$(env | grep -c "$DUMMY")
wrote=0; ( : > "$T/siblings/planted" ) 2>/dev/null && wrote=1
read_=0; cat "$T/fakehome/.ssh/id" >/dev/null 2>&1 && read_=1
echo "10.\$leak.\$wrote.\$read_"
EOS
  chmod +x "$WORK/web/node_modules/.bin/stryker"
  printf '#!/bin/sh\n[ "$1 $2" = "--no-install stryker" ] && exec ./node_modules/.bin/stryker "$3"\nexit 1\n' > "$STUBS/npx"
  chmod +x "$STUBS/npx"
  write web/package.json '{}'
  write web/src/a.ts
  run_it "$(snap "$(cls js web/src/a.ts)")"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(lang js | jq -r .status)" = runnable ]
  # The tool ran (a version came back) and could do none of it.
  [ "$(lang js | jq -r .version)" = "10.0.0.0" ] || [ "$(lang js | jq -r .version)" = "10.0.0" ] || { echo "version: $(lang js | jq -r .version)"; return 1; }
  [ ! -e "$T/siblings/planted" ]
}

@test "detect refuses to probe when no sandbox can be built, and says why" {
  rm -f "$SYS/sandbox-exec" "$SYS/bwrap"
  stub gremlins "gremlins version 0.6.0"
  write pkg/a.go
  run_it "$(snap "$(cls go pkg/a.go)")"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no isolation available"* ]]
  [[ "$stderr" != *"install them"* ]]
}

@test "a language-free run needs no sandbox" {
  rm -f "$SYS/sandbox-exec" "$SYS/bwrap"
  run_it "$(snap '{"source_files_by_language":{}}')"
  [ "$status" -eq 0 ]
  [ "$(field .skip_reason)" = "no changed source files" ]
}
