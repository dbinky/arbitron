package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

type staticProfiles map[string]string

func (p staticProfiles) ProfileKind(alias string) (string, bool) { k, ok := p[alias]; return k, ok }

// A group sample whose resolverPromptFile is an @lib path. The boot helper must
// check that path against the SHARED library: a missing file, or a copy that
// exists only in the seed owner's private library, cannot make a globally
// published sample look self-contained.
const libResolverSample = `{"name":"grp","description":"lib resolver","author":"t","inputs":[],
 "nodes":[
  {"id":"fan","type":"parallel_group","entrypoint":true,"config":{"group":{
     "form":"branches","tasks":[{"key":"a","entry":"a1"}],"completionRule":"all_succeed",
     "unfinishedBehavior":"cancel_unfinished","gatherMode":"results_and_files",
     "resolverProfile":"default-agent","resolverPromptFile":"@lib/prompts/resolve.md"}}},
  {"id":"a1","type":"transform","config":{"operations":[{"variable":"a","operation":"set","value":1}]}},
  {"id":"done","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
 ],
 "edges":[
  {"source":"fan","target":"a1","output":"task","label":"a"},
  {"source":"fan","target":"done","output":"success"},
  {"source":"fan","target":"failed","output":"failure"}]}`

// seedLibResolver boots the seeding helper over a source tree holding the group
// sample, optionally shipping the library file, and reports whether the sample
// was seeded.
func seedLibResolver(t *testing.T, shipLib, privateLib bool) bool {
	t.Helper()
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "grp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "grp", "workflow.json"), []byte(libResolverSample), 0o644))
	if shipLib {
		require.NoError(t, os.MkdirAll(filepath.Join(src, "_lib", "prompts"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(src, "_lib", "prompts", "resolve.md"), []byte("resolve"), 0o644))
	}
	ws := t.TempDir()
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	if privateLib {
		priv := filepath.Join(ws, "users", owner.String(), "_lib", "prompts")
		require.NoError(t, os.MkdirAll(priv, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(priv, "resolve.md"), []byte("private only"), 0o644))
	}
	repo := filesystem.NewWorkflowRepo(ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	profiles := staticProfiles{"default-agent": "agent"}
	require.NoError(t, seedSampleWorkflows(context.Background(), src, ws, repo, &recordingPublished{t: t}, nil, nil, nil,
		filesystem.NewLibraryStore(ws), filesystem.NewFileReader(ws), owner, logger, profiles))
	_, err := repo.Load(context.Background(), workflow.Shared(), "grp")
	return err == nil
}

func TestSeedSampleWorkflows_LibResolverFileIsCheckedAgainstTheSharedLibrary(t *testing.T) {
	assert.True(t, seedLibResolver(t, true, false), "a resolver file shipped in the shared library is found")
	assert.False(t, seedLibResolver(t, false, false), "a missing @lib resolver file must keep the sample out")
	assert.False(t, seedLibResolver(t, false, true),
		"a private-only copy under the seed owner must not satisfy a globally published sample")
}
