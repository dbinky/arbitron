#!/bin/sh
# Resume detector: decides which pipeline stage a resumed run should start at by
# looking at what docs already exist in the checked-out repo.
#
# WHY A SCRIPT AND NOT AN LLM NODE: "does a spec exist for this slug" is a
# deterministic filesystem question. An `llm_call` node has no tools (it cannot
# list files), and its outputSchema is NOT enforced on the claude-cli chat path,
# so routing the whole pipeline on it would be a coin flip. This script produces
# the facts; an llm_call is used ONLY to disambiguate when this script reports
# ambiguous:true, and its verdict is advisory over `best` below.
#
# FUZZY MATCHING: the caller's slug often differs from the slug baked into the
# doc filenames (e.g. resume with "app-push-notifications" against docs written
# as "2026-07-25-push-notifications-spec.md"). We normalize every filename by
# stripping the leading ISO date and the role suffix, tokenize on "-", drop
# filler tokens, and score by token overlap in both directions. That matches
# app-push-notifications -> push-notifications at 1.0 without any model call.
#
# Reads {work_dir, slug} from stdin JSON. Emits:
#   {resume_stage, matched_slug, spec_path, design_count, plan_count,
#    has_spec, has_designs, has_plans, ambiguous, best_score, candidates}
# resume_stage is one of: spec | design | plans | implementation
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug // ""')"

[ -n "$WORK" ] || { echo "detect-docs: work_dir not set" >&2; exit 1; }

# List *.md basenames in a docs subdir; empty JSON array when the dir is absent.
# Recurses: plan sets are sometimes nested one level (docs/plans/<feature>/...),
# and a maxdepth-1 scan reported those repos as having no plans at all.
list_dir() {
  if [ -d "$WORK/$1" ]; then
    find "$WORK/$1" -type f -name '*.md' 2>/dev/null \
      | sed 's|.*/||' | sort -u | jq -R . | jq -s .
  else
    echo '[]'
  fi
}

SPECS="$(list_dir docs/specs)"
DESIGNS="$(list_dir docs/designs)"
PLANS="$(list_dir docs/plans)"

jq -n \
  --arg slug "$SLUG" \
  --argjson specs "$SPECS" \
  --argjson designs "$DESIGNS" \
  --argjson plans "$PLANS" '
  # Tokens that carry no identifying signal and would otherwise depress scores.
  def filler: ["app","the","a","an","of","for","and","pretinder","feature"];

  # Strip .md, a leading ISO date, and the role suffix -> the bare feature slug.
  def bare:
      sub("\\.md$"; "")
    | sub("^[0-9]{4}-[0-9]{2}-[0-9]{2}-"; "")
    | sub("-implementation-phase-[0-9]+-task-[0-9]+$"; "")
    | sub("-implementation-phase-[0-9]+$"; "")
    | sub("-design-phase-[0-9]+$"; "")
    | sub("-plan-phase-[0-9]+$"; "")
    | sub("-spec$"; "")
    | sub("-design$"; "")
    | sub("-plan$"; "");

  def toks:
      ascii_downcase | split("-")
    | map(select(length > 0))
    | map(select(. as $t | (filler | index($t)) == null));

  ($slug | toks) as $want

  # Build one record per file: which kind it is, its bare slug, its tokens.
  | ( ($specs   | map({kind:"spec",   file:.}))
    + ($designs | map({kind:"design", file:.}))
    + ($plans   | map({kind:"plan",   file:.}))
    ) | map(. + {bare: (.file | bare)})
      | map(. + {toks: (.bare | toks)})

  # Group by bare slug -> one candidate per feature present in the repo.
  | group_by(.bare)
  | map(
      (.[0].bare) as $b
      | (.[0].toks) as $have
      | ($have | map(select(. as $x | ($want | index($x)) != null)) | length) as $hits
      | {
          slug: $b,
          # Score in both directions and keep the stronger reading, so neither a
          # longer caller slug nor a longer doc slug is penalised on its own.
          score: (if ($have|length) == 0 or ($want|length) == 0 then 0
                  else ([ ($hits / ($have|length)), ($hits / ($want|length)) ] | max)
                  end),
          spec_files:   [ .[] | select(.kind=="spec")   | .file ],
          design_files: [ .[] | select(.kind=="design") | .file ],
          plan_files:   [ .[] | select(.kind=="plan")   | .file ]
        }
    )
  | sort_by(-.score) as $cands

  | ($cands | map(select(.score >= 0.6))) as $viable
  | ($viable | first) as $best
  | (($viable | length) > 1
     and (($viable[0].score - $viable[1].score) < 0.2)) as $tie

  | ($best // {slug:"", score:0, spec_files:[], design_files:[], plan_files:[]}) as $b
  | (($b.spec_files   | length) > 0) as $has_spec
  | (($b.design_files | length) > 0) as $has_designs
  | (($b.plan_files   | length) > 0) as $has_plans

  | {
      # The ladder. Plans imply spec+designs were already consumed to build them,
      # so plans alone are enough to jump straight to implementation.
      resume_stage:
        (if   $has_plans           then "implementation"
         elif $has_spec and $has_designs then "plans"
         elif $has_spec            then "design"
         else                           "spec"
         end),
      matched_slug: $b.slug,
      # safe_slug is what callers should promote into the `slug` variable.
      # matched_slug is "" when nothing scored high enough, and blindly adopting
      # that empties `slug` — which yields a "feature" placeholder branch and
      # misnamed docs. Fall back to the slug the caller passed, so promoting
      # this value is always safe and no conditional guard is needed downstream.
      safe_slug:    (if ($b.slug | length) > 0 then $b.slug else $slug end),
      spec_path:    (if $has_spec then "docs/specs/" + ($b.spec_files | first) else "" end),
      design_count: ($b.design_files | length),
      plan_count:   ($b.plan_files   | length),
      has_spec:     $has_spec,
      has_designs:  $has_designs,
      has_plans:    $has_plans,
      best_score:   $b.score,
      # Only true when two different features score close enough that picking the
      # wrong one is plausible. The llm_call tiebreaker runs ONLY in this case.
      ambiguous:    $tie,
      candidates:   ($cands | map({slug, score,
                                   specs: (.spec_files|length),
                                   designs: (.design_files|length),
                                   plans: (.plan_files|length)}))
    }
'
