# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

- **[Area 1] Create manifest write is not atomic** — `WorkflowRepo.Create` writes `workflow.json` directly with `os.WriteFile` (no temp-file-rename). Unlike `Update` and `SaveNodePositions`, a crash mid-write leaves a partial manifest. The directory still exists, so the next `Create` call returns "already exists" instead of succeeding. `Update` and `WriteFile` both use atomic writes. Fix: use temp-file-rename in `Create` as well.

## Fixed Previously

- **[Area 1] WriteFile lacked atomic writes** — `WorkflowRepo.WriteFile` was using `os.WriteFile` directly, which can leave existing files in a corrupted state if the process crashes mid-overwrite. Fixed by switching to the temp-file-rename pattern already used by `Update` and `SaveNodePositions`. Test added: `TestWorkflowRepo_WriteFile_AtomicWrite` verifies no `.tmp` files remain and the correct content is written.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_
