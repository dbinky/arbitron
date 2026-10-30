#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  LIB="$BATS_TEST_DIRNAME"
  SCRIPT="$LIB/commit-leftovers.sh"
  GATE="$LIB/tamper-gate.sh"
  COUNT="$LIB/../implement/scripts/count-plan-steps.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WS" "$WORK/pkg" "$WORK/docs/plans"
  git init -q -b main "$WORK"
  printf 'package pkg\n\nfunc A() int { return 1 }\n' > "$WORK/pkg/a.go"
  printf 'package pkg\n\nfunc TestA(t *testing.T) {\n\tt.Errorf("a")\n\tt.Errorf("b")\n}\n' > "$WORK/pkg/a_test.go"
  printf -- '- [ ] task one\n- [ ] task two\n' > "$WORK/docs/plans/p.md"
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m base
  BASE="$(git -C "$WORK" rev-parse HEAD)"
}

snap() { jq -n --arg w "$WORK" --arg ws "$WS" '{work_dir:$w, workspace:$ws, "resolve-inputs":{inputs:{plan_paths:["docs/plans/p.md"]}}}'; }
leftovers() { run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$(snap)" "$SCRIPT" "$@"; }
j() { printf '%s' "$output" | jq -r "$1"; }

@test "a clean tree commits nothing" {
  leftovers
  [ "$status" -eq 0 ]
  [ "$(j .committed)" = false ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
}

@test "modified, staged and untracked leftovers are committed after the base" {
  printf '// more\n' >> "$WORK/pkg/a.go"
  printf 'package pkg\n' > "$WORK/pkg/new.go"
  printf 'x\n' >> "$WORK/pkg/a_test.go"
  git -C "$WORK" add pkg/a_test.go
  leftovers
  [ "$status" -eq 0 ]
  [ "$(j .committed)" = true ]
  [ "$(j '.files|length')" = "3" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
  [ "$(git -C "$WORK" rev-list --count "$BASE..HEAD")" = "1" ]
  [ "$(j .head)" = "$(git -C "$WORK" rev-parse HEAD)" ]
  git -C "$WORK" log -1 --format=%B | grep -q '\[skip ci\]'
}

@test "ignored files are left alone" {
  printf 'ignored.log\n' > "$WORK/.gitignore"
  git -C "$WORK" add .gitignore; git -C "$WORK" commit -q -m ignore
  printf 'x\n' > "$WORK/ignored.log"
  leftovers
  [ "$(j .committed)" = false ]
}

@test "a failing commit fails the pass" {
  printf '// more\n' >> "$WORK/pkg/a.go"
  # An index lock makes git add / commit fail.
  : > "$WORK/.git/index.lock"
  leftovers
  [ "$status" -ne 0 ]
}

# A git status that cannot read the tree must not read as "clean".
@test "a corrupt index fails closed instead of reporting a clean tree" {
  printf 'garbage, not an index\n' > "$WORK/.git/index"
  leftovers
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *"commit-leftovers: git status failed"* ]]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
}

@test "a corrupt index fails closed even with leftovers present" {
  printf '// more\n' >> "$WORK/pkg/a.go"
  printf 'garbage, not an index\n' > "$WORK/.git/index"
  leftovers
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *"git status failed"* ]]
}

@test "a git status that fails after the commit fails closed" {
  printf '// more\n' >> "$WORK/pkg/a.go"
  # A git shim whose second status call (the post-commit cleanliness check) fails.
  REAL_GIT="$(command -v git)"
  mkdir -p "$T/shim"
  cat > "$T/shim/git" <<EOF
#!/bin/sh
for a in "\$@"; do
  if [ "\$a" = status ]; then
    n=\$(( \$(cat '$T/status.count' 2>/dev/null || echo 0) + 1 ))
    echo "\$n" > '$T/status.count'
    [ "\$n" -ge 2 ] && { echo "fatal: index file corrupt" >&2; exit 128; }
    break
  fi
done
exec '$REAL_GIT' "\$@"
EOF
  chmod +x "$T/shim/git"
  run --separate-stderr env PATH="$T/shim:$PATH" bash -c 'printf "%s" "$1" | sh "$2"' _ "$(snap)" "$SCRIPT"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *"git status failed"* ]]
  [ "$(cat "$T/status.count")" = 2 ]
  # The commit itself happened; only the verification could not be made.
  [ "$(git -C "$WORK" rev-list --count "$BASE..HEAD")" = 1 ]
}

@test "a truncated index (boundary: empty file) fails closed" {
  : > "$WORK/.git/index"
  leftovers
  [ "$status" -ne 0 ]
  [[ "$stderr" == *"git status failed"* ]]
}

@test "not a git worktree is an error" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ '{"work_dir":"/nonexistent-dir-xyz"}' "$SCRIPT"
  [ "$status" -ne 0 ]
}

# Producer to sink: an agent that edits source and ticks plan boxes but forgets
# to commit must still be judged by the gate, counted as complete, and leave the
# implementation in the commit the stage records.
@test "uncommitted agent edits reach the gate diff, the plan counter and HEAD" {
  printf 'package pkg\n\nfunc A() int { return 2 }\n' > "$WORK/pkg/a.go"
  printf 'package pkg\n\nfunc TestA(t *testing.T) {\n\tt.Errorf("a")\n\tt.Errorf("b")\n\tt.Skip("later")\n}\n' > "$WORK/pkg/a_test.go"
  printf -- '- [x] task one\n- [x] task two\n' > "$WORK/docs/plans/p.md"

  leftovers
  [ "$status" -eq 0 ]
  [ "$(j .committed)" = true ]

  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "$3" HEAD' _ "$(snap)" "$GATE" "$BASE"
  [ "$status" -eq 0 ]
  [ "$(j '.clean')" = false ]
  [ "$(j '[.findings[].kind]|index("skip_marker_added")!=null')" = true ]

  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$(snap)" "$COUNT"
  [ "$(j .all_tasks_complete)" = true ]

  [ "$(git -C "$WORK" show HEAD:pkg/a.go | grep -c 'return 2')" = "1" ]
  [ "$(git -C "$WORK" show HEAD:docs/plans/p.md | grep -c '\[x\]')" = "2" ]
}
