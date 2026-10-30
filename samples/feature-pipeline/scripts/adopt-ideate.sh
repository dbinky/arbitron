#!/bin/sh
# Fail-closed adoption of the ideate child's result. Reads the snapshot on stdin
# and emits {branch, slug} from ideate.childOutput.finish.outputs; a missing,
# empty or non-string value exits 1.
set -eu

jq -ce '
  .ideate.childOutput.finish.outputs as $o
  | if ($o.branch | type) == "string" and $o.branch != ""
       and ($o.slug | type) == "string" and $o.slug != ""
    then {branch: $o.branch, slug: $o.slug}
    else error("ideate returned no branch/slug") end
' 2>/dev/null || { echo "adopt-ideate: ideate returned no branch/slug" >&2; exit 1; }
