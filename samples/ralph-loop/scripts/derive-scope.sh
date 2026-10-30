#!/bin/sh
# Derives WHAT this branch changed, so the focus-area planner reviews the right
# systems instead of the whole repo.
#
# THE SHALLOW-CLONE PROBLEM: init-workspace.sh clones with `--depth 1
# --branch=<base_branch>`, so the object store contains exactly one commit of one
# branch. `origin/main` does not exist and `git merge-base HEAD main` fails. Any
# scope derivation must fetch the default branch itself first, and must survive
# the case where the fork point is still deeper than what it managed to fetch.
#
# Three strategies, tried in order, with the method reported so downstream stages
# know how trustworthy the scope is:
#   merge-base   — true fork point found; diff is exactly this branch's work
#   two-dot      — fork point unreachable; diff vs the default branch TIP, which
#                  also shows changes that landed on the default branch since
#                  the fork (over-broad, but never under-broad)
#   branch-only  — no default branch reachable at all; falls back to the tip
#                  commit's own diff
#
# When pr_base is set (a stacked PR targets it rather than the default branch) it
# is fetched and used in place of the default branch in all three strategies. An
# unreachable pr_base falls back to the default branch, reported via the method.
#
# Reads {work_dir, base_branch, pr_base, scope_note} from stdin JSON. Emits
# {default_branch, scope_method, merge_base, file_count, changed_files,
#  files_truncated, commit_log, scope_note}.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
NOTE="$(printf '%s' "$IN" | jq -r '.scope_note // ""')"
PR_BASE="$(printf '%s' "$IN" | jq -r '.pr_base // ""')"
[ -n "$WORK" ] || { echo "derive-scope: work_dir not set" >&2; exit 1; }
# pr_base is a workflow input that reaches git argv: reject option injection and
# malformed refs before any git call.
if [ -n "$PR_BASE" ]; then
  case "$PR_BASE" in
    -*) echo "derive-scope: pr_base must not begin with '-': $PR_BASE" >&2; exit 1 ;;
  esac
  git check-ref-format "refs/heads/$PR_BASE" >/dev/null 2>&1 || \
    { echo "derive-scope: pr_base is not a valid branch name: $PR_BASE" >&2; exit 1; }
fi

MAX_FILES=300

# Default branch: prefer what the remote actually advertises, then the usual
# suspects. `remote show` needs network but is authoritative for repos that use
# something other than main/master.
DEF="$(git -C "$WORK" remote show origin 2>/dev/null | sed -n 's/.*HEAD branch: //p' | head -1)"
if [ -z "$DEF" ]; then
  for c in main master; do
    if git -C "$WORK" ls-remote --exit-code --heads origin "$c" >/dev/null 2>&1; then DEF="$c"; break; fi
  done
fi
[ -n "$DEF" ] || DEF="main"

# pr_base replaces the default branch when it is actually fetchable.
if [ -n "$PR_BASE" ]; then
  if git -C "$WORK" fetch --quiet --depth=200 origin "+refs/heads/$PR_BASE:refs/remotes/origin/$PR_BASE" 2>/dev/null; then
    DEF="$PR_BASE"
  else
    echo "derive-scope: pr_base '$PR_BASE' was unreachable and is being IGNORED; scope is derived against $DEF" >&2
  fi
fi

# Fetch the default branch. Bounded depth first — cheap and enough for almost any
# real feature branch — then unshallow only if the fork point is still missing.
git -C "$WORK" fetch --quiet --depth=200 origin "+refs/heads/$DEF:refs/remotes/origin/$DEF" 2>/dev/null || true
MB="$(git -C "$WORK" merge-base HEAD "origin/$DEF" 2>/dev/null || true)"
if [ -z "$MB" ]; then
  git -C "$WORK" fetch --quiet --unshallow origin 2>/dev/null || \
    git -C "$WORK" fetch --quiet --deepen=1000 origin 2>/dev/null || true
  MB="$(git -C "$WORK" merge-base HEAD "origin/$DEF" 2>/dev/null || true)"
fi

if [ -n "$MB" ]; then
  METHOD="merge-base"
  FILES="$(git -C "$WORK" diff --name-only "$MB..HEAD" 2>/dev/null || true)"
  LOG="$(git -C "$WORK" log --oneline "$MB..HEAD" 2>/dev/null | head -100 || true)"
elif git -C "$WORK" rev-parse --verify --quiet "origin/$DEF" >/dev/null 2>&1; then
  METHOD="two-dot"
  FILES="$(git -C "$WORK" diff --name-only "origin/$DEF" HEAD 2>/dev/null || true)"
  LOG="$(git -C "$WORK" log --oneline -50 2>/dev/null || true)"
else
  METHOD="branch-only"
  FILES="$(git -C "$WORK" show --name-only --pretty=format: HEAD 2>/dev/null | sed '/^$/d' || true)"
  LOG="$(git -C "$WORK" log --oneline -50 2>/dev/null || true)"
fi

# NOTE: the trailing newline matters. `printf '%s'` emits none, so `wc -l` counts
# one fewer line than there are files — and a single-file branch would then count
# 0 and be rejected below as an empty scope.
COUNT="$(printf '%s\n' "$FILES" | sed '/^$/d' | wc -l | tr -d ' ')"
TRUNC=false
if [ "$COUNT" -gt "$MAX_FILES" ]; then
  FILES="$(printf '%s' "$FILES" | head -"$MAX_FILES")"
  TRUNC=true
fi

# A scope of zero files means the planner has nothing to reason about; that is a
# wiring or input error (wrong branch, already merged), not a valid empty result.
if [ "$COUNT" -eq 0 ]; then
  echo "derive-scope: no changed files found for HEAD vs origin/$DEF (method=$METHOD)." >&2
  echo "  The branch may already be merged, may be the default branch itself, or" >&2
  echo "  base_branch may be wrong. Refusing to seed a review plan with no scope." >&2
  exit 1
fi

jq -n \
  --arg def "$DEF" --arg m "$METHOD" --arg mb "$MB" \
  --arg files "$FILES" --arg log "$LOG" --arg note "$NOTE" \
  --argjson count "$COUNT" --argjson trunc "$TRUNC" \
  '{default_branch:$def, scope_method:$m, merge_base:$mb,
    file_count:$count, files_truncated:$trunc,
    changed_files:($files | split("\n") | map(select(length>0))),
    commit_log:$log, scope_note:$note}'
