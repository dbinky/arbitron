#!/usr/bin/env bats

setup() {
  SKELETON="$BATS_TEST_DIRNAME/idea-doc-skeleton.md"
}

@test "has the header fields" {
  for f in '**Status:**' '**Written:**' '**Scope:**' '**Explicitly out of scope:**'; do
    grep -qF "$f" "$SKELETON"
  done
}

@test "has every section in order" {
  run grep '^## ' "$SKELETON"
  [ "$status" -eq 0 ]
  expected='## 1. Why this exists
## 2. What exists today (grounding)
## 3. The idea
## 4. Proposal
## 5. Design tensions and gotchas
## 6. Decisions locked
## 7. Non-goals (for now)
## 8. Open questions
## 9. Suggested sequencing
## Appendix — related'
  [ "$output" = "$expected" ]
}

@test "contains no template markers" {
  [ -f "$SKELETON" ]
  run grep -c '{{\|}}' "$SKELETON"
  [ "$output" = "0" ]
}
