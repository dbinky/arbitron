# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none — all identified issues have been resolved)_

## Fixed Previously

- **[Area 4] `TestWorkflowValidate` missing domain error test** — added "load error returned as tool error" sub-test: repo stub returns an error from `Load`, verifying the handler surfaces it as a tool error with the original message.

- **[Area 4] `TestWorkflowDeleteFile` missing validation failure tests** — added "missing name returns error" and "missing path returns error" sub-cases; also tightened the existing domain error assertion to verify the error message text.

- **[Area 4] Several workflow tool tests lacked full 5-category coverage** — `TestWorkflowUpdate` had no domain error test and no invalid-manifest-JSON test. `TestWorkflowDelete` had no domain error test. `TestWorkflowListFiles` and `TestWorkflowReadFile` had no missing-name test. All four gaps fixed by adding the missing test cases.

- **[Area 4] `workflow_write_file` rejected empty content, blocking valid empty-file writes** — Fixed `workflowWriteFileHandler` to check key presence (`_, ok := args["content"]`) rather than value non-emptiness (`content == ""`), so absent content is rejected but empty-string content is forwarded to the repo. Added `TestWorkflowWriteFile/empty_content_writes_empty_file` (edge case) and `TestWorkflowWriteFile/domain_error_returned_as_tool_error` (error scenario).

- **[Area 3] `Listen` method had no test** — Added `TestServer_Listen_StdinEOF` (edge: immediate EOF returns cleanly) and `TestServer_Listen_ContextCancellation` (error: context cancel + stdin close forces shutdown) to `adapter/mcp/server_test.go`, using `os.Pipe()` as the documented testing transport.

- **[Area 3] `Subscriptions()` method was never tested** — Added `TestServer_Subscriptions_NilWhenNoEventBus` (nil deps, nil EventBus → nil SubscriptionManager) and `TestServer_Subscriptions_NonNilWithEventBus` (valid EventBus → non-nil SubscriptionManager) to `adapter/mcp/server_test.go`.

- **[Area 2] DeleteFile handler lacked path-traversal 400 response** — `DeleteFile` had no check for `"invalid file path"` from the repo, so traversal attempts returned 500. Fixed by adding the check (matching `WriteFile`) and adding `TestWorkflowHandler_DeleteFile/returns_400_for_path_traversal`.

- **[Area 2] WriteFile handler checked wrong error string for path traversal** — Handler used `strings.Contains(err.Error(), "traversal")` but the filesystem repo returns `"invalid file path: ..."`. Real traversal attempts returned 500. Fixed by changing the check to `"invalid file path"`. Test mock updated from `"path traversal detected"` to `"invalid file path: ../../etc/passwd"` to match actual repo behavior.

- **[Area 1] WriteFile lacked atomic writes** — `WorkflowRepo.WriteFile` was using `os.WriteFile` directly, which can leave existing files in a corrupted state if the process crashes mid-overwrite. Fixed by switching to the temp-file-rename pattern already used by `Update` and `SaveNodePositions`. Test added: `TestWorkflowRepo_WriteFile_AtomicWrite`.

- **[Area 1] Create manifest write was not atomic** — `WorkflowRepo.Create` wrote `workflow.json` directly without temp-file-rename. Fixed by writing to `workflow.json.tmp` first and renaming, with full rollback (`RemoveAll`) on either step's failure. Test added: `TestWorkflowRepo_Create_AtomicManifestWrite`.

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_
