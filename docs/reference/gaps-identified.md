# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

- **[Area 1] WriteFile lacked atomic writes** — `WorkflowRepo.WriteFile` was using `os.WriteFile` directly, which can leave existing files in a corrupted state if the process crashes mid-overwrite. Fixed by switching to the temp-file-rename pattern already used by `Update` and `SaveNodePositions`. Test added: `TestWorkflowRepo_WriteFile_AtomicWrite`.

- **[Area 1] Create manifest write was not atomic** — `WorkflowRepo.Create` wrote `workflow.json` directly without temp-file-rename. Fixed by writing to `workflow.json.tmp` first and renaming, with full rollback (`RemoveAll`) on either step's failure. Test added: `TestWorkflowRepo_Create_AtomicManifestWrite`.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_
