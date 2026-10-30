package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// An install seeded from the base revision holds the OLD ralph-loop, which
// declares none of the inputs feature-pipeline maps into its refine stage.
// The seeder never overwrites a name already in its ledger, so seeding the new
// samples adds the new parent beside the old child. Triggering the parent must
// then fail up front, with an actionable message, before any parent node runs.
func TestSeedUpgrade_OldChildNewParentIsRejectedBeforeAnyParentExecution(t *testing.T) {
	samplesDir, err := filepath.Abs(filepath.Join("..", "..", "samples"))
	require.NoError(t, err)

	// 1. The base revision's source tree: ralph-loop without the inputs the
	// new parent maps (in_place, focus_seed, baseline_path), plus the library.
	oldSrc := t.TempDir()
	require.NoError(t, os.CopyFS(filepath.Join(oldSrc, "_lib"), os.DirFS(filepath.Join(samplesDir, "_lib"))))
	require.NoError(t, os.CopyFS(filepath.Join(oldSrc, "ralph-loop"), os.DirFS(filepath.Join(samplesDir, "ralph-loop"))))
	raw, err := os.ReadFile(filepath.Join(oldSrc, "ralph-loop", "workflow.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	added := map[string]bool{"in_place": true, "focus_seed": true, "baseline_path": true}
	var kept []any
	for _, in := range doc["inputs"].([]any) {
		if !added[in.(map[string]any)["name"].(string)] {
			kept = append(kept, in)
		}
	}
	doc["inputs"] = kept
	raw, err = json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(oldSrc, "ralph-loop", "workflow.json"), raw, 0o644))

	ws := t.TempDir()
	repo := filesystem.NewWorkflowRepo(ws)
	lib := filesystem.NewLibraryStore(ws)
	files := filesystem.NewFileReader(ws)
	pub := &recordingPublished{t: t}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := context.Background()

	// 2. Boot on the base revision, then boot again on the new samples.
	require.NoError(t, seedSampleWorkflows(ctx, oldSrc, ws, repo, pub, nil, nil, nil, lib, files, owner, logger))
	require.NoError(t, seedSampleWorkflows(ctx, samplesDir, ws, repo, pub, nil, nil, nil, lib, files, owner, logger))

	oldChild, err := repo.Load(ctx, workflow.Shared(), "ralph-loop")
	require.NoError(t, err)
	for _, in := range oldChild.Inputs {
		require.False(t, added[in.Name], "the ledger must have kept the installed ralph-loop, found input %q", in.Name)
	}
	_, err = repo.Load(ctx, workflow.Shared(), "feature-pipeline")
	require.NoError(t, err, "the new parent must have been seeded beside the old child")

	// 3. Triggering the parent is refused before anything is created or run. The
	// engine has no instance repository, queue or executors, so reaching any of
	// them (creating the run, enqueuing, executing a node) would panic.
	eng := engine.NewEngine(repo, nil, nil, nil, nil, nil, nil, nil, nil, "")
	runCtx := identity.WithIdentity(ctx, identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser})
	id, started, err := eng.Trigger(runCtx, "feature-pipeline", map[string]any{"repo_url": "https://example.test/r.git"}, "normal")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrSpawnUnknownChildInput)
	assert.Equal(t, uuid.Nil, id)
	assert.False(t, started)
	for _, want := range []string{"in_place", "focus_seed", "baseline_path", `"ralph-loop"`, "update and publish"} {
		assert.Contains(t, err.Error(), want)
	}
}

// The composable parent shipped as feature-pipeline-next and the monolith as
// feature-pipeline; the rename swaps those directories (the monolith becomes
// legacy-feature-pipeline). The seeder matches by name through its ledger, so an
// install seeded before the rename must keep whatever it installed under
// feature-pipeline: the new parent is never written over the monolith, nor the
// monolith over a parent, and the legacy name is only ever added.
func TestSeedUpgrade_PipelineRenameNeverReplacesAnInstalledFeaturePipeline(t *testing.T) {
	samplesDir, err := filepath.Abs(filepath.Join("..", "..", "samples"))
	require.NoError(t, err)

	// preRename builds the pre-rename source tree: the monolith under
	// feature-pipeline and, when withNext, the parent under feature-pipeline-next.
	preRename := func(t *testing.T, withNext bool) string {
		src := t.TempDir()
		for _, d := range []string{"_lib", "ideate", "spec", "design", "plan", "implement", "harden", "ralph-loop", "pr-review"} {
			require.NoError(t, os.CopyFS(filepath.Join(src, d), os.DirFS(filepath.Join(samplesDir, d))))
		}
		place := func(from, to string) {
			require.NoError(t, os.CopyFS(filepath.Join(src, to), os.DirFS(filepath.Join(samplesDir, from))))
			path := filepath.Join(src, to, "workflow.json")
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))
			doc["name"] = to
			raw, err = json.MarshalIndent(doc, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0o644))
		}
		place("legacy-feature-pipeline", "feature-pipeline")
		if withNext {
			place("feature-pipeline", "feature-pipeline-next")
		}
		return src
	}
	hasNode := func(wf *workflow.Workflow, id string) bool {
		for _, n := range wf.Nodes {
			if n.ID == id {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		name     string
		withNext bool
	}{
		{"monolith only", false},
		{"monolith and feature-pipeline-next", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			repo := filesystem.NewWorkflowRepo(ws)
			lib := filesystem.NewLibraryStore(ws)
			files := filesystem.NewFileReader(ws)
			pub := &recordingPublished{t: t}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			ctx := context.Background()

			require.NoError(t, seedSampleWorkflows(ctx, preRename(t, tc.withNext), ws, repo, pub, nil, nil, nil, lib, files, owner, logger))
			installed, err := repo.Load(ctx, workflow.Shared(), "feature-pipeline")
			require.NoError(t, err)
			require.True(t, hasNode(installed, "deps-gate"), "the pre-rename install must hold the monolith")

			require.NoError(t, seedSampleWorkflows(ctx, samplesDir, ws, repo, pub, nil, nil, nil, lib, files, owner, logger))

			kept, err := repo.Load(ctx, workflow.Shared(), "feature-pipeline")
			require.NoError(t, err)
			assert.True(t, hasNode(kept, "deps-gate"), "the installed monolith must survive the upgrade")
			assert.False(t, hasNode(kept, "intake-check"), "the new parent must not be written over the installed feature-pipeline")
			legacy, err := repo.Load(ctx, workflow.Shared(), "legacy-feature-pipeline")
			require.NoError(t, err, "the upgrade adds the legacy name")
			assert.True(t, hasNode(legacy, "deps-gate"))
			next, err := repo.Load(ctx, workflow.Shared(), "feature-pipeline-next")
			if tc.withNext {
				require.NoError(t, err)
				assert.True(t, hasNode(next, "intake-check"), "an installed feature-pipeline-next keeps the parent")
			} else {
				assert.ErrorIs(t, err, workflow.ErrNotFound, "the upgrade never seeds the retired name")
			}
		})
	}
}
