#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  LIB="$BATS_TEST_DIRNAME"
  CLEAR="$LIB/clear-owned-docs.sh"
  LIST="$LIB/../plan/scripts/list-plans.sh"
  FINALIZE="$LIB/finalize-docs.sh"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$BATS_TEST_TMPDIR/work"
  git init -q -b main "$WORK"
  mkdir -p "$WORK/docs/plans/feat" "$WORK/docs/plans/other" "$WORK/docs/designs"
  D=2026-10-24
}

snap() { jq -nc --arg w "$WORK" --arg d "$D" '{work_dir:$w, plan_dir:"docs/plans/feat", run_date:$d}'; }
clear_() { run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$(snap)" "$CLEAR" "$@"; }
sh_() { printf '%s' "$(snap)" | sh "$@"; }
mk() { for f in "$@"; do mkdir -p "$WORK/$(dirname "$f")"; echo "$f" > "$WORK/$f"; done; }
commit() { git -C "$WORK" add -A; git -C "$WORK" commit -q -m "${1:-c}"; }
PLANS='docs/plans/feat/2026-10-24-phase-*-task-*.md'

@test "removes only today's plans for this slug and commits the removal" {
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md docs/plans/feat/2026-10-24-phase-2-task-1.md \
     docs/plans/feat/2026-10-23-phase-1-task-1.md docs/plans/other/2026-10-24-phase-1-task-1.md docs/plans/feat/notes.md
  commit
  clear_ --message "docs: clear" "$PLANS"
  [ "$status" -eq 0 ]
  [ "$(jq -c .removed <<<"$output")" = '["docs/plans/feat/2026-10-24-phase-1-task-1.md","docs/plans/feat/2026-10-24-phase-2-task-1.md"]' ]
  [ "$(jq -r .committed <<<"$output")" = true ]
  [ ! -e "$WORK/docs/plans/feat/2026-10-24-phase-1-task-1.md" ]
  [ -e "$WORK/docs/plans/feat/2026-10-23-phase-1-task-1.md" ]
  [ -e "$WORK/docs/plans/other/2026-10-24-phase-1-task-1.md" ]
  [ -e "$WORK/docs/plans/feat/notes.md" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
  [ "$(git -C "$WORK" log -1 --format=%s)" = "docs: clear" ]
  # History keeps the earlier set.
  git -C "$WORK" show HEAD~1:docs/plans/feat/2026-10-24-phase-2-task-1.md >/dev/null
}

@test "untracked matches are deleted without a commit" {
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md
  before="$(git -C "$WORK" rev-parse HEAD 2>/dev/null || echo none)"
  clear_ --message m "$PLANS"
  [ "$status" -eq 0 ]
  [ "$(jq -r .committed <<<"$output")" = false ]
  [ ! -e "$WORK/docs/plans/feat/2026-10-24-phase-1-task-1.md" ]
  [ "$(git -C "$WORK" rev-parse HEAD 2>/dev/null || echo none)" = "$before" ]
}

@test "nothing to clear is a no-op" {
  mk docs/plans/feat/notes.md
  commit
  head="$(git -C "$WORK" rev-parse HEAD)"
  clear_ --message m "$PLANS"
  [ "$status" -eq 0 ]
  [ "$(jq -c .removed <<<"$output")" = '[]' ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$head" ]
}

@test "patterns that are not a stage's own output are refused" {
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md
  commit
  for bad in '*' 'docs/*' 'docs/plans/feat/*.md' 'docs/plans/*/2026-10-24-phase-*-task-*.md' '/etc/*' \
             'docs/plans/../../x/2026-10-24-phase-*-task-*.md' 'docs/designs/*.md' 'src/2026-10-24-phase-*-task-*.md'; do
    clear_ --message m "$bad"
    [ "$status" -ne 0 ] || { echo "accepted: $bad"; return 1; }
  done
  [ -e "$WORK/docs/plans/feat/2026-10-24-phase-1-task-1.md" ]
}

@test "a missing --message or glob is refused" {
  clear_ "$PLANS"
  [ "$status" -ne 0 ]
  clear_ --message m
  [ "$status" -ne 0 ]
}

@test "symlinks are left alone" {
  mk docs/plans/feat/real.md
  ln -s real.md "$WORK/docs/plans/feat/2026-10-24-phase-1-task-1.md"
  commit
  clear_ --message m "$PLANS"
  [ "$status" -eq 0 ]
  [ -L "$WORK/docs/plans/feat/2026-10-24-phase-1-task-1.md" ]
}

# --- Same-day rerun with fewer phases: the published set is the new set only

@test "plan: a same-day rerun with fewer phases publishes only the replacement through list-plans" {
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md docs/plans/feat/2026-10-24-phase-2-task-1.md docs/plans/feat/2026-10-24-phase-2-task-2.md
  commit "first run"
  # Without the clear step the old extras are published beside the replacement.
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md
  out="$(sh_ "$LIST")"
  [ "$(jq -r '.plan_paths|length' <<<"$out")" = 3 ]
  # With it, only the rerun's files remain.
  clear_ --message m "$PLANS"
  [ "$status" -eq 0 ]
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md
  commit "second run"
  out="$(sh_ "$LIST")"
  [ "$(jq -c .plan_paths <<<"$out")" = '["docs/plans/feat/2026-10-24-phase-1-task-1.md"]' ]
}

@test "plan: a rerun that produces nothing fails list-plans instead of publishing the old set" {
  mk docs/plans/feat/2026-10-24-phase-1-task-1.md
  commit
  clear_ --message m "$PLANS"
  [ "$status" -eq 0 ]
  run bash -c 'printf "%s" "$1" | sh "$2"' _ "$(snap)" "$LIST"
  [ "$status" -ne 0 ]
}

@test "design: a same-day rerun with fewer phases commits and publishes only the replacement" {
  mk docs/designs/2026-10-24-feat-design-phase-1.md docs/designs/2026-10-24-feat-design-phase-2.md docs/designs/2026-10-23-feat-design-phase-1.md
  commit "first run"
  DG='docs/designs/2026-10-24-feat-design-phase-*.md'
  clear_ --message m "$DG"
  [ "$status" -eq 0 ]
  [ "$(jq -c .removed <<<"$output")" = '["docs/designs/2026-10-24-feat-design-phase-1.md","docs/designs/2026-10-24-feat-design-phase-2.md"]' ]
  mk docs/designs/2026-10-24-feat-design-phase-1.md
  echo changed >> "$WORK/docs/designs/2026-10-24-feat-design-phase-1.md"
  out="$(sh_ "$FINALIZE" --message "docs: add designs" "$DG")"
  [ "$(jq -c .paths <<<"$out")" = '["docs/designs/2026-10-24-feat-design-phase-1.md"]' ]
  [ -e "$WORK/docs/designs/2026-10-23-feat-design-phase-1.md" ]
}

@test "design: a rerun that writes nothing fails finalize-docs" {
  mk docs/designs/2026-10-24-feat-design-phase-1.md
  commit
  DG='docs/designs/2026-10-24-feat-design-phase-*.md'
  clear_ --message m "$DG"
  [ "$status" -eq 0 ]
  run bash -c 'printf "%s" "$1" | sh "$2" --message x "$3"' _ "$(snap)" "$FINALIZE" "$DG"
  [ "$status" -ne 0 ]
}
