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

// ownerScopedFileReader keys files by owner, workflow and path. A read under
// the wrong scope misses, and every read is recorded for provenance assertions.
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
	fr.put(owner, "other-wf", "prompts/resolve.md", "OTHER WORKFLOW prompt")

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
	fr.put(uuid.New(), "wf", "_rules.md", "OTHER USER rules")
	fr.put(owner, "other-wf", "_rules.md", "OTHER WORKFLOW rules")

	out, err := engine.LoadGroupResolverPrompt(fr, owner, "wf", "resolve.md", samplePromptData())
	require.NoError(t, err)
	assert.Equal(t, "Shared rules for /ws/.group-backing/run-1/staging/gather-1.\nResolve b.", out)
	assert.Equal(t, []string{
		ownerFileKey(owner, "wf", "resolve.md"),
		ownerFileKey(owner, "wf", "_rules.md"),
	}, fr.readKeys(), "includes retain the parent's owner and workflow scope")
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
		"git add -- <path>",
		"summary",
	} {
		assert.Contains(t, p, rule)
	}
}
