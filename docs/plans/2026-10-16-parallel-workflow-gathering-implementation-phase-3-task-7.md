# Parallel Workflow Gathering — Phase 3 Task 7: Embedded default resolver prompt + override loader

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Ship the default conflict-resolution prompt as an embedded asset and a loader that substitutes the persisted conflict data into it, or into an author's workflow-relative override read through the owner-scoped `FileReader`.
**Architecture:** One asset `domain/engine/assets/group_resolver_prompt.md` embedded with `//go:embed`, and one domain file `domain/engine/group_resolver_prompt.go` with `LoadGroupResolverPrompt`. An override is read through the existing `loadTemplate` (`domain/engine/template_include.go:159`) so it gets the same owner-scoped read and `{{include:...}}` expansion as every other prompt; substitution uses the existing `variable.Resolve` over a store holding ONLY the resolution data (never the parent's variables).
**Tech Stack:** Go 1.25.5, `embed`, `domain/variable`, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Gathering attempts automatic conflict resolution. Unresolved conflicts ... open an interactive resolution session"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §4 ("Load the embedded default prompt or workflow-relative override through scoped `FileReader.ReadFile` / `loadTemplate` ... Supply persisted conflict paths, task key, baseline and merge refs, and the active area"), Data Provenance rows "Default prompt" (P3-C asset) and "Author prompt bytes".

**Interfaces**
- Consumes (existing code):
  - `domain/engine/ports.go:20-24` `type FileReader interface { ReadFile(owner uuid.UUID, workflowName, relativePath string) ([]byte, error); ResolvePath(...); FileExists(...) }`.
  - `domain/engine/template_include.go:159` `func loadTemplate(fr FileReader, owner uuid.UUID, workflowName, templateFile string) (string, error)` (same package; expands includes, bounded).
  - `domain/variable/variable.go:38` `func NewStore() *VariableStore`, `variable.go:100` `func (s *VariableStore) SetInput(key string, value any) error`, `domain/variable/substitution.go:13` `func Resolve(input string, store *VariableStore) string`.
- Produces (exact):
  - `type GroupResolverPromptData struct { TaskKey string; BaselineRef string; MergeRef string; ConflictPaths []string; WorkDir string; Attempt int }`
  - `func LoadGroupResolverPrompt(fr FileReader, owner uuid.UUID, workflowName, overridePath string, data GroupResolverPromptData) (string, error)`
  - `func DefaultGroupResolverPrompt() string`
  - `var ErrGroupResolverPrompt = errors.New("engine: group resolver prompt")`
  - Template variables available to the default prompt AND to author overrides: `{{taskKey}}`, `{{baselineRef}}`, `{{mergeRef}}`, `{{conflictPaths}}` (a Markdown bullet list, one path per line), `{{workDir}}`, `{{attempt}}`.
  - Test fake reused BY NAME by P3-T8: `ownerScopedFileReader` / `newOwnerScopedFileReader` (in `domain/engine/group_resolver_prompt_test.go`, package `engine_test`).

**Wiring & data provenance**
- Composition root: none of its own. It becomes live through `GroupResolutionBridge.StartResolution` (P3-T8), wired in `cmd/arbitron/main.go` by **P3-T11** with the real `fileReader` (`filesystem.NewFileReader(cfg.Workspace.Path)`, main.go:449).
- Producers:
  - `fr` — `adapter/filesystem/file_reader.go:FileReaderAdapter` (main.go:449) passed to `engine.NewGroupResolutionBridge` (P3-T11).
  - `owner` / `workflowName` — the PARENT instance (`instance.Instance.OwnerID`, `.WorkflowName`) the bridge receives from `FileGatherer.startSession` (P3-T6).
  - `overridePath` — `GroupRun.Config["resolverPromptFile"]` (snapshot of P1-T1 `workflow.GroupConfig.ResolverPromptFile`, persisted by P2-T8 `GroupLauncher.Launch`; its existence is checked at authoring time by P1-T3 `GROUP_MISSING_PROMPT_FILE`).
  - `data.TaskKey/MergeRef/ConflictPaths/Attempt` — `engine.ResolutionRequest` built by `FileGatherer.startSession` (P3-T6) from the persisted `GatherReport` attempt; `data.WorkDir` — `GatherHandle.StagingDir` returned by `WorkingArea.BeginGather` (P3-T3); `data.BaselineRef` — the literal revision `MERGE_HEAD^` that P3-T8 passes (the conflicted task commit's single parent is the baseline, and it is resolvable inside the staging repository, unlike the snapshot handle or the backing store's refs).
- Provenance probes: `TestLoadGroupResolverPrompt_OverrideReadThroughFileReaderScopedToOwner` (fails if the owner is not passed through to the reader), `TestLoadGroupResolverPrompt_RejectsIncompleteData` (fails if an empty producer value silently yields a prompt with no scope).

### Task 7: Resolver prompt asset and loader
**Files:**
- Create: `domain/engine/assets/group_resolver_prompt.md`
- Create: `domain/engine/group_resolver_prompt.go`
- Test:   `domain/engine/group_resolver_prompt_test.go` (package `engine_test`)

No existing file is modified (`domain/engine` has no `assets/` directory yet; this task creates it).

- [x] Step 1: Write the failing tests. Create `domain/engine/group_resolver_prompt_test.go`:

```go
package engine_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerScopedFileReader is an engine.FileReader keyed by (owner, workflow,
// path), so a read under the wrong owner MISSES — unlike mockFileReader, which
// ignores the owner. It records every read for provenance assertions.
type ownerScopedFileReader struct {
	mu    sync.Mutex
	files map[string][]byte
	reads []string
}

var _ engine.FileReader = (*ownerScopedFileReader)(nil)

func newOwnerScopedFileReader() *ownerScopedFileReader {
	return &ownerScopedFileReader{files: make(map[string][]byte)}
}

func ownerFileKey(owner uuid.UUID, workflowName, path string) string {
	return owner.String() + "|" + workflowName + "|" + path
}

func (r *ownerScopedFileReader) put(owner uuid.UUID, workflowName, path, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[ownerFileKey(owner, workflowName, path)] = []byte(content)
}

func (r *ownerScopedFileReader) ReadFile(owner uuid.UUID, workflowName, path string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ownerFileKey(owner, workflowName, path)
	r.reads = append(r.reads, key)
	b, ok := r.files[key]
	if !ok {
		return nil, fmt.Errorf("%s: %w", path, os.ErrNotExist)
	}
	return b, nil
}

func (r *ownerScopedFileReader) ResolvePath(owner uuid.UUID, workflowName, path string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.files[ownerFileKey(owner, workflowName, path)]; !ok {
		return "", os.ErrNotExist
	}
	return "/files/" + workflowName + "/" + path, nil
}

func (r *ownerScopedFileReader) FileExists(owner uuid.UUID, workflowName, path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.files[ownerFileKey(owner, workflowName, path)]
	return ok
}

func (r *ownerScopedFileReader) readKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reads...)
}

func samplePromptData() engine.GroupResolverPromptData {
	return engine.GroupResolverPromptData{
		TaskKey:       "b",
		BaselineRef:   "git:run-1:base-commit",
		MergeRef:      "refs/arbitron/tasks/task-b",
		ConflictPaths: []string{"shared.txt", "docs/readme.md"},
		WorkDir:       "/ws/.group-backing/run-1/staging/gather-1",
		Attempt:       2,
	}
}

// --- Happy path -------------------------------------------------------------

func TestLoadGroupResolverPrompt_DefaultUsedWhenOverrideEmpty(t *testing.T) {
	fr := newOwnerScopedFileReader()

	out, err := engine.LoadGroupResolverPrompt(fr, uuid.New(), "wf", "", samplePromptData())
	require.NoError(t, err)

	assert.Contains(t, out, "task `b`")
	assert.Contains(t, out, "attempt 2")
	assert.Contains(t, out, "`/ws/.group-backing/run-1/staging/gather-1`")
	assert.Contains(t, out, "`git:run-1:base-commit`")
	assert.Contains(t, out, "`refs/arbitron/tasks/task-b`")
	assert.Contains(t, out, "- shared.txt\n- docs/readme.md")
	assert.NotContains(t, out, "{{", "every template variable is substituted")
	assert.Empty(t, fr.readKeys(), "the embedded default never touches the workflow's files")
}

func TestLoadGroupResolverPrompt_WhitespaceOverrideMeansDefault(t *testing.T) {
	out, err := engine.LoadGroupResolverPrompt(newOwnerScopedFileReader(), uuid.New(), "wf", "   ", samplePromptData())
	require.NoError(t, err)
	assert.Contains(t, out, "Work only inside")
}

func TestLoadGroupResolverPrompt_NilReaderIsFineForTheDefault(t *testing.T) {
	out, err := engine.LoadGroupResolverPrompt(nil, uuid.New(), "wf", "", samplePromptData())
	require.NoError(t, err)
	assert.Contains(t, out, "task `b`")
}

func TestLoadGroupResolverPrompt_OverrideReadThroughFileReaderScopedToOwner(t *testing.T) {
	fr := newOwnerScopedFileReader()
	owner, other := uuid.New(), uuid.New()
	fr.put(owner, "wf", "prompts/resolve.md", "OWNER prompt for {{taskKey}} in {{workDir}}:\n{{conflictPaths}}\n(attempt {{attempt}}, {{mergeRef}} vs {{baselineRef}})")
	fr.put(other, "wf", "prompts/resolve.md", "OTHER USER prompt")

	out, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "prompts/resolve.md", samplePromptData())
	require.NoError(t, err)

	assert.Equal(t, "OWNER prompt for b in /ws/.group-backing/run-1/staging/gather-1:\n- shared.txt\n- docs/readme.md\n(attempt 2, refs/arbitron/tasks/task-b vs git:run-1:base-commit)", out)
	assert.Equal(t, []string{ownerFileKey(owner, "wf", "prompts/resolve.md")}, fr.readKeys(),
		"the override is read under the PARENT OWNER's scope, never another user's")
}

func TestLoadGroupResolverPrompt_OverrideIncludesExpand(t *testing.T) {
	fr := newOwnerScopedFileReader()
	owner := uuid.New()
	fr.put(owner, "wf", "resolve.md", "{{include:_rules.md}}\nResolve {{taskKey}}.")
	fr.put(owner, "wf", "_rules.md", "Shared rules for {{workDir}}.")

	out, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "resolve.md", samplePromptData())
	require.NoError(t, err)
	assert.Equal(t, "Shared rules for /ws/.group-backing/run-1/staging/gather-1.\nResolve b.", out)
}

func TestLoadGroupResolverPrompt_UnknownVariablesStayLiteral(t *testing.T) {
	fr := newOwnerScopedFileReader()
	owner := uuid.New()
	fr.put(owner, "wf", "resolve.md", "Fix {{taskKey}}; parent vars like {{repo}} are not available.")

	out, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "resolve.md", samplePromptData())
	require.NoError(t, err)
	assert.Equal(t, "Fix b; parent vars like {{repo}} are not available.", out,
		"only resolution data is substituted; the parent's variables never leak into the prompt")
}

func TestLoadGroupResolverPrompt_AttemptDefaultsToOne(t *testing.T) {
	data := samplePromptData()
	data.Attempt = 0
	out, err := engine.LoadGroupResolverPrompt(nil, uuid.New(), "wf", "", data)
	require.NoError(t, err)
	assert.Contains(t, out, "attempt 1")
}

// --- Error ------------------------------------------------------------------

func TestLoadGroupResolverPrompt_MissingOverrideIsAnError(t *testing.T) {
	fr := newOwnerScopedFileReader()

	_, err := engine.LoadGroupResolverPrompt(fr, uuid.New(), "wf", "prompts/missing.md", samplePromptData())
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupResolverPrompt)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Contains(t, err.Error(), `"prompts/missing.md"`)
}

func TestLoadGroupResolverPrompt_OverrideWithoutReaderIsAnError(t *testing.T) {
	_, err := engine.LoadGroupResolverPrompt(nil, uuid.New(), "wf", "resolve.md", samplePromptData())
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupResolverPrompt)
	assert.Contains(t, err.Error(), "no file reader")
}

func TestLoadGroupResolverPrompt_EmptyOverrideIsAnError(t *testing.T) {
	fr := newOwnerScopedFileReader()
	owner := uuid.New()
	fr.put(owner, "wf", "blank.md", "  \n\t")

	_, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "blank.md", samplePromptData())
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupResolverPrompt)
	assert.Contains(t, err.Error(), "empty")
}

func TestLoadGroupResolverPrompt_BrokenIncludeIsAnError(t *testing.T) {
	fr := newOwnerScopedFileReader()
	owner := uuid.New()
	fr.put(owner, "wf", "resolve.md", "{{include:nope.md}}")

	_, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "resolve.md", samplePromptData())
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupResolverPrompt)
	assert.ErrorIs(t, err, engine.ErrTemplateInclude)
}

// --- Edge / provenance ------------------------------------------------------

func TestLoadGroupResolverPrompt_RejectsIncompleteData(t *testing.T) {
	cases := map[string]func(*engine.GroupResolverPromptData){
		"no task key":       func(d *engine.GroupResolverPromptData) { d.TaskKey = "" },
		"no staging dir":    func(d *engine.GroupResolverPromptData) { d.WorkDir = " " },
		"no conflict paths": func(d *engine.GroupResolverPromptData) { d.ConflictPaths = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			data := samplePromptData()
			mutate(&data)
			_, err := engine.LoadGroupResolverPrompt(nil, uuid.New(), "wf", "", data)
			require.Error(t, err)
			assert.True(t, errors.Is(err, engine.ErrGroupResolverPrompt))
		})
	}
}

func TestDefaultGroupResolverPrompt_CarriesTheSafetyRules(t *testing.T) {
	p := engine.DefaultGroupResolverPrompt()
	require.NotEmpty(t, strings.TrimSpace(p))
	for _, rule := range []string{
		"Work only inside `{{workDir}}`",
		"Never touch another task's working area",
		"Do not run Git commands that change refs",
		"Remove every conflict marker",
		"git add -- <path>", // P3-T3: a path is resolved only once it is staged
		"summary",
	} {
		assert.Contains(t, p, rule)
	}
}
```

- [x] Step 2: Run them and confirm they fail.

```bash
go test ./domain/engine/ -run 'TestLoadGroupResolverPrompt|TestDefaultGroupResolverPrompt' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_resolver_prompt_test.go:...: undefined: engine.GroupResolverPromptData
domain/engine/group_resolver_prompt_test.go:...: undefined: engine.LoadGroupResolverPrompt
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Create the asset `domain/engine/assets/group_resolver_prompt.md` (complete text):

````markdown
# Parallel group gather — conflict resolution (task `{{taskKey}}`, attempt {{attempt}})

You are resolving merge conflicts for an Arbitron parallel group. Each task in
the group worked on its own isolated copy of the parent's working area. Their
changes are being gathered one task at a time, in the workflow author's task
order, into a private staging copy of the parent's working area. Merging the
changes of task `{{taskKey}}` produced conflicts that could not be resolved
automatically. Your job is to resolve them in the staging copy.

## Where you are working

- Staging directory (your working directory): `{{workDir}}`
- Common starting point of every task (baseline): `{{baselineRef}}`
- Changes being merged now (task `{{taskKey}}`): `{{mergeRef}}` (inspect it as
  `MERGE_HEAD` in the staging directory)
- Conflicted paths, relative to the staging directory:

{{conflictPaths}}

## What to do

1. Open each conflicted path listed above. Each one contains Git-style conflict
   markers: `<<<<<<<` starts the side that was already gathered, `=======`
   separates it from the side of task `{{taskKey}}`, and `>>>>>>>` ends the
   conflict.
2. Edit each file so that it correctly combines BOTH sides: keep the intent of
   the changes already gathered and the intent of task `{{taskKey}}`.
   Remove every conflict marker.
3. Check that none of the listed paths still contains `<<<<<<<`, `=======` or
   `>>>>>>>` lines. If two changes genuinely cannot both be kept, keep the one
   that leaves the project working and say why. For a binary file, keep one
   side whole (read-only `git show :2:<path>` is the gathered side,
   `git show :3:<path>` the side of task `{{taskKey}}`; write it into place).
4. Mark each resolved path as resolved by running `git add -- <path>` in
   `{{workDir}}`. A path counts as resolved only once it is staged AND free of
   conflict markers; an edited but unstaged path is still conflicted.
5. Reply with a short summary: for each path, what each side changed and how
   you combined them. Then stop and wait for the operator.

## Rules

- Work only inside `{{workDir}}`. Do not read or modify any other directory.
- Never touch another task's working area, the parent's live working area, or
  any other workflow run: other tasks may still be running.
- Edit only the conflicted paths listed above. If a listed file cannot be
  resolved without a matching change to another file in the staging
  directory, make the smallest such change and say exactly what you changed
  and why.
- Do not create, delete or rename files other than the listed paths.
- Do not run Git commands that change refs or history: no `git commit`,
  `git merge`, `git rebase`, `git reset`, `git checkout`, `git switch`,
  `git restore`, `git stash`, `git rm`, `git branch`, `git tag`, `git push`,
  `git pull` or `git fetch`. The only Git write you may run is
  `git add -- <path>` on a listed path you resolved (step 4). Read-only
  inspection such as `git diff`, `git log` or `git show` is allowed. Arbitron
  commits your resolution itself.
- Do not install packages, start long-running processes, or access the network.

## Finishing

The operator reviews your summary and may ask for changes. When the operator
confirms the resolution, or asks you to finish, emit the completion block
described below with a JSON object containing a `summary` string and a
`resolvedPaths` array. Arbitron then verifies that no conflicts remain; if any
do, a fresh resolution attempt is opened.
````

- [x] Step 4: Implement the loader. Create `domain/engine/group_resolver_prompt.go`:

```go
package engine

import (
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
)

// defaultGroupResolverPrompt is the conflict-resolution prompt used when a
// parallel_group sets no resolverPromptFile. It is embedded so the feature
// works with no workflow-side files at all.
//
//go:embed assets/group_resolver_prompt.md
var defaultGroupResolverPrompt string

// ErrGroupResolverPrompt marks a failure to build the resolution prompt
// (missing/unreadable/empty override, or incomplete resolution data). The
// resolution session is not started; the gatherer aborts and rolls back.
var ErrGroupResolverPrompt = errors.New("engine: group resolver prompt")

// GroupResolverPromptData is the persisted conflict data a resolution prompt
// is rendered with. It is deliberately the ONLY data the prompt sees: the
// parent's variables are not substituted, so a prompt can never leak them.
type GroupResolverPromptData struct {
	TaskKey       string   // task whose merge conflicted
	BaselineRef   string   // common starting point of every task (snapshot handle)
	MergeRef      string   // finalized ref of the task being merged
	ConflictPaths []string // conflicted paths, relative to WorkDir
	WorkDir       string   // the staging directory the agent works in
	Attempt       int      // 1-based resolution attempt for this merge
}

// DefaultGroupResolverPrompt returns the embedded default prompt template.
func DefaultGroupResolverPrompt() string { return defaultGroupResolverPrompt }

// LoadGroupResolverPrompt renders the resolution prompt. With an empty
// overridePath the embedded default is used. Otherwise the override is read
// through loadTemplate under the PARENT OWNER's scope (private first, then
// shared, confined to the workflow directory) with {{include:...}} expanded,
// exactly like every other prompt file. The template variables {{taskKey}},
// {{baselineRef}}, {{mergeRef}}, {{conflictPaths}} (Markdown bullet list),
// {{workDir}} and {{attempt}} are substituted; anything else stays literal.
func LoadGroupResolverPrompt(fr FileReader, owner uuid.UUID, workflowName, overridePath string, data GroupResolverPromptData) (string, error) {
	if strings.TrimSpace(data.TaskKey) == "" {
		return "", fmt.Errorf("%w: task key is required", ErrGroupResolverPrompt)
	}
	if strings.TrimSpace(data.WorkDir) == "" {
		return "", fmt.Errorf("%w: staging directory is required", ErrGroupResolverPrompt)
	}
	if len(data.ConflictPaths) == 0 {
		return "", fmt.Errorf("%w: at least one conflicted path is required", ErrGroupResolverPrompt)
	}

	text := defaultGroupResolverPrompt
	if path := strings.TrimSpace(overridePath); path != "" {
		if fr == nil {
			return "", fmt.Errorf("%w: resolverPromptFile %q is configured but no file reader is available", ErrGroupResolverPrompt, path)
		}
		loaded, err := loadTemplate(fr, owner, workflowName, path)
		if err != nil {
			return "", fmt.Errorf("%w: read resolverPromptFile %q: %w", ErrGroupResolverPrompt, path, err)
		}
		text = loaded
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%w: prompt is empty", ErrGroupResolverPrompt)
	}

	attempt := data.Attempt
	if attempt < 1 {
		attempt = 1
	}
	store := variable.NewStore()
	inputs := []struct {
		key   string
		value string
	}{
		{"taskKey", data.TaskKey},
		{"baselineRef", data.BaselineRef},
		{"mergeRef", data.MergeRef},
		{"conflictPaths", bulletList(data.ConflictPaths)},
		{"workDir", data.WorkDir},
		{"attempt", strconv.Itoa(attempt)},
	}
	for _, in := range inputs {
		if err := store.SetInput(in.key, in.value); err != nil {
			return "", fmt.Errorf("%w: %w", ErrGroupResolverPrompt, err)
		}
	}
	return variable.Resolve(text, store), nil
}

func bulletList(items []string) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, "- "+it)
	}
	return strings.Join(lines, "\n")
}
```

- [x] Step 5: Run the tests and confirm they pass.

```bash
go test ./domain/engine/ -run 'TestLoadGroupResolverPrompt|TestDefaultGroupResolverPrompt' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 6: Whole package, gofmt, vet.

```bash
go test ./domain/engine/ -count=1
gofmt -l domain/engine/group_resolver_prompt.go domain/engine/group_resolver_prompt_test.go
go vet ./domain/engine/
```
Expected: `ok  	github.com/arbitron/arbitron/domain/engine	...`; no output from `gofmt -l` / `go vet`.

- [x] Completion gate: Prove the prompt reaches a real resolution session and downstream gathered-file sink through the production composition root.

> RESOLVED: P3-T8 and P3-T11 now supply the bridge and production wiring (P3-T11 commit `62faa02`). `initDomain` constructs `NewGroupResolutionBridge` with the real file reader, profile resolver, stream selector, repositories and engine notifier, passes it to `NewGroupGatherer`, and registers that gatherer before freezing executors. `FileGatherer.startSession` feeds persisted conflict data and the real staging handle to `StartResolution`, which calls `LoadGroupResolverPrompt` under the parent owner's workflow scope. No new implementation was needed in this completion pass; the original red/green implementation steps remain complete.

Completion verification: the focused loader/default-prompt tests, whole engine package, gofmt and engine vet pass. `TestGroupFilesE2E_AutomaticRepairContinuesWithoutOperator` passes with `-race` through `initDomain`: the subprocess consumes the loaded prompt, checks actual staging Git refs, edits/stages the conflicted file, and the downstream `check-ok` node persists `shared="combined"` and `a="alpha"` in the parent variables. The session and resolved attempt are persisted, with no human conflict notification.

Completion self-review, in order:
1. Spec compliance: reviewed the asset, loader and production bridge against this task's exact template/data/scope contract. All six resolution variables, embedded fallback, workflow-relative includes, isolated variable store and wrapped errors remain intact; no extra behavior or options were added.
2. Anti-green-theater: temporarily removed the loaded prompt from the bridge's first-turn message while leaving the sentinel intact. The real-root test failed on the persisted failure route and absent downstream `check-ok` output. Restored the prompt and reran the same race test, green. Owner/workflow-decoy and include tests also pass. No negative-control mutation remains.
3. Code quality: checked neighboring `loadTemplate` conventions, formatting, vet and clean diff. Reran the whole engine suite and production-root race test after review, green.

```bash
go test ./domain/engine/ -run 'TestLoadGroupResolverPrompt|TestDefaultGroupResolverPrompt' -count=1
go test ./domain/engine/ -count=1
gofmt -l domain/engine/group_resolver_prompt.go domain/engine/group_resolver_prompt_test.go
go vet ./domain/engine/
go test ./cmd/arbitron/ -run '^TestGroupFilesE2E_AutomaticRepairContinuesWithoutOperator$' -race -count=1
```

This completion pass changes only this plan's verification record and closes its former wiring blocker; it does not start another task.

Initial implementation self-review (historical; runtime blocker now resolved above): spec compliance — only the specified embedded asset, loader and reusable owner-scoped test fake were added; anti-green-theater — owner/workflow decoys and exact rendered prompt/include-read assertions reject missing inputs at the loader seam; code quality — reuses loadTemplate and variable.Resolve, preserves wrapped errors, and documents BaselineRef as a staging-resolvable revision rather than the backing snapshot handle.

- [x] Step 7: Commit.

```bash
git add domain/engine/assets/group_resolver_prompt.md domain/engine/group_resolver_prompt.go domain/engine/group_resolver_prompt_test.go
git commit -m "feat(engine): embedded default conflict-resolution prompt with owner-scoped override" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
