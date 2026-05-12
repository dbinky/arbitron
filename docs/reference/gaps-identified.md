# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

- **[Area 2] DeleteFile handler lacks path-traversal 400 response** — `DeleteFile` has no path-traversal detection branch. If the repo rejects a path with `"invalid file path: ..."`, the handler falls through to 500 instead of 400. Fix: add `strings.Contains(err.Error(), "invalid file path")` check before the catch-all 500, matching the pattern now used in `WriteFile`.

## Fixed Previously

- **[Area 2] WriteFile handler checked wrong error string for path traversal** — Handler used `strings.Contains(err.Error(), "traversal")` but the filesystem repo returns `"invalid file path: ..."`. Real traversal attempts returned 500. Fixed by changing the check to `"invalid file path"`. Test mock updated from `"path traversal detected"` to `"invalid file path: ../../etc/passwd"` to match actual repo behavior.

- **[Area 1] WriteFile lacked atomic writes** — `WorkflowRepo.WriteFile` was using `os.WriteFile` directly, which can leave existing files in a corrupted state if the process crashes mid-overwrite. Fixed by switching to the temp-file-rename pattern already used by `Update` and `SaveNodePositions`. Test added: `TestWorkflowRepo_WriteFile_AtomicWrite`.

- **[Area 1] Create manifest write was not atomic** — `WorkflowRepo.Create` wrote `workflow.json` directly without temp-file-rename. Fixed by writing to `workflow.json.tmp` first and renaming, with full rollback (`RemoveAll`) on either step's failure. Test added: `TestWorkflowRepo_Create_AtomicManifestWrite`.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_
