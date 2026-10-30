#!/bin/sh
# Applies the mode rules after the clone and resolves base_branch. Reads the
# snapshot on stdin (intake.mode, clone.work_dir, clone.branch, plus the
# top-level branch, slug, idea_path, start_at, base_branch) and emits
# {slug, branch, work_dir, idea_path, record_idea, base_branch}.
#
# resume: slug discovered from the single manifest in .arbitron/pipeline when
#   empty (a manifest is a JSON object with a non-empty string slug and an array
#   of stages; the <slug>.baseline.json, .harden.json and .survivors.json the
#   stages store beside it are not manifests);
#   a given idea_path must exist and is recorded; with no idea_path, no done
#   ideate entry and no start_at it fails. base_branch: explicit, else the
#   manifest's top-level base_branch, else exit 1.
# idea:   idea_path must exist in the new worktree; recorded.
# fresh:  nothing to record; no branch yet.
set -eu

die() { echo "intake-check: $*" >&2; exit 1; }

IN="$(cat)"
get() { printf '%s' "$IN" | jq -r "$1"; }

MODE="$(get '.intake.mode // ""')"
WORK="$(get '.clone.work_dir // ""')"
SLUG="$(get '.slug // ""')"
IDEA="$(get '.idea_path // ""')"
START="$(get '.start_at // ""')"
BASE="$(get '.base_branch // ""')"

case "$MODE" in resume|idea|fresh) ;; *) die "unknown intake mode: $MODE" ;; esac
[ -n "$WORK" ] && [ -d "$WORK" ] || die "clone.work_dir is not a directory"

case "$IDEA" in
  /*|..|../*|*/..|*/../*) die "idea_path must be a relative path inside the repo: $IDEA" ;;
esac

BRANCH=""
RECORD=false
OUT_IDEA=""

case "$MODE" in
  fresh) ;;
  idea)
    BRANCH="$(get '.clone.branch // ""')"
    [ -n "$BRANCH" ] || die "clone produced no branch"
    [ -f "$WORK/$IDEA" ] || die "idea_path not found on base: $IDEA"
    RECORD=true
    OUT_IDEA="$IDEA" ;;
  resume)
    BRANCH="$(get '.branch // ""')"
    [ -n "$BRANCH" ] || die "branch not set"
    DIR="$WORK/.arbitron/pipeline"
    if [ -z "$SLUG" ]; then
      N=0
      for f in "$DIR"/*.json; do
        [ -f "$f" ] || continue
        jq -e 'type == "object" and (.slug | type == "string" and length > 0) and (.stages | type == "array")' "$f" >/dev/null 2>&1 || continue
        N=$((N + 1))
        FOUND="$(basename "$f" .json)"
      done
      [ "$N" -eq 1 ] || die "found $N manifests on $BRANCH; pass slug"
      SLUG="$FOUND"
    fi
    case "$SLUG" in */*|.*) die "slug must not contain '/' or start with '.': $SLUG" ;; esac
    MANIFEST="$DIR/$SLUG.json"
    HAS_IDEATE=false
    MBASE=""
    if [ -f "$MANIFEST" ]; then
      HAS_IDEATE="$(jq -r 'any((.stages // [])[]; .stage == "ideate" and .status == "done")' "$MANIFEST")" \
        || die "malformed manifest: $MANIFEST"
      MBASE="$(jq -r '.base_branch // "" | if type == "string" then . else "" end' "$MANIFEST")"
    fi
    if [ -n "$IDEA" ]; then
      [ -f "$WORK/$IDEA" ] || die "idea_path not found on branch $BRANCH: $IDEA"
      RECORD=true
      OUT_IDEA="$IDEA"
    elif [ "$HAS_IDEATE" != true ] && [ -z "$START" ]; then
      die "resume branch has no idea doc; pass idea_path or start_at=spec"
    fi
    if [ -z "$BASE" ]; then
      [ -n "$MBASE" ] || die "resume branch has no recorded base_branch; pass base_branch"
      BASE="$MBASE"
    fi ;;
esac

jq -n --arg slug "$SLUG" --arg branch "$BRANCH" --arg work "$WORK" --arg idea "$OUT_IDEA" \
  --argjson rec "$RECORD" --arg base "$BASE" \
  '{slug:$slug, branch:$branch, work_dir:$work, idea_path:$idea, record_idea:$rec, base_branch:$base}'
