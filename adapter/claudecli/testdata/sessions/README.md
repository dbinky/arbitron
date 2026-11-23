Claude Code 2.1.x session fixtures (layout from docs/evidence/remote-runners/research-session-migration.md §2.1).
Placeholders replaced by the tests when installing into a fake HOME:
- `{{PROJECT_DIR}}` — absolute `<config>/projects/<encoded-workdir>` of the installing HOME
- `{{SESSION_ID}}` — c623adbe-4edc-468c-9256-0157d5025a02
`basic/` has no absolute references. `sidecar/` references its tool-results file by absolute path, like real sessions do.
