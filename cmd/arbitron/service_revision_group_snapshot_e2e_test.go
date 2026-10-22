package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Inject a source edit at the precheck/acquisition boundary, then retain the
// real coordinator, repository, registry, journal and SQLite arrangement sink.
type groupImportBoundary struct {
	workflow.MutationCoordinator
	before func()
}

func (m *groupImportBoundary) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	if m.before != nil {
		before := m.before
		m.before = nil
		before()
	}
	return m.MutationCoordinator.WithMutations(ctx, targets, fn)
}

// Missing snapshot comparison must fail even when an external edit preserves
// the saved revision, or adds groups to an originally ordinary definition.
func TestServiceRevisionE2E_GroupImportRejectsChangedSource(t *testing.T) {
	for _, op := range []string{"publish", "copy", "duplicate", "rename"} {
		for _, sourceKind := range []string{"group", "legacy-group", "ordinary"} {
			t.Run(op+"/"+sourceKind, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{})
				ds, _ := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				var child, parent workflow.Workflow
				require.NoError(t, json.Unmarshal([]byte(groupEchoChildManifest), &child))
				require.NoError(t, json.Unmarshal([]byte(groupWorkflowsManifest), &parent))
				require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, &child))
				initial := &parent
				if sourceKind == "ordinary" {
					initial = layoutE2EWorkflow(parent.Name, "ordinary before precheck")
				}
				require.NoError(t, ds.workflowSvc.Create(ctx, parent.Name, initial))
				name, sourcePath := parent.Name, e.privatePath(parent.Name)
				if op == "copy" {
					require.NoError(t, ds.workflowSvc.Publish(ctx, name, "shared-parent", workflow.VisibilityPublic, ""))
					name, sourcePath = "shared-parent", e.sharedPath("shared-parent")
				}
				original := layoutE2EManifest(t, sourcePath)
				if sourceKind == "legacy-group" {
					original.Presentation = nil
					b, err := json.Marshal(original)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(sourcePath, b, 0600))
				}
				before, err := os.ReadFile(sourcePath)
				require.NoError(t, err)
				var rowCount int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rowCount))
				var changed []byte
				ds.workflowSvc.SetMutationCoordinator(&groupImportBoundary{MutationCoordinator: ds.layout.mutations, before: func() {
					parent.Name, parent.ID, parent.Presentation = original.Name, original.ID, original.Presentation
					parent.Nodes[1].Config.Group.Tasks = nil
					var err error
					changed, err = json.Marshal(&parent)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(sourcePath, changed, 0600))
				}})
				call := func() error {
					switch op {
					case "publish":
						return ds.workflowSvc.Publish(ctx, name, "target", workflow.VisibilityPublic, "")
					case "copy":
						return ds.workflowSvc.Copy(ctx, name, "target")
					case "duplicate":
						return ds.workflowSvc.Duplicate(ctx, name, "target")
					default:
						return ds.workflowSvc.Rename(ctx, name, "target")
					}
				}
				require.ErrorIs(t, call(), workflow.ErrMutationPending)
				after, err := os.ReadFile(sourcePath)
				require.NoError(t, err)
				require.Equal(t, changed, after, "refusal must preserve the intervening edit")
				require.NoDirExists(t, filepath.Dir(e.privatePath("target")))
				require.NoDirExists(t, filepath.Dir(e.sharedPath("target")))
				require.Zero(t, registryRows(t, ds, "target"))
				var afterRows int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&afterRows))
				require.Equal(t, rowCount, afterRows)
				entries, err := ds.layout.journal.Entries(ctx)
				require.NoError(t, err)
				require.Empty(t, entries)
				intents, err := ds.layout.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Empty(t, intents)
				// A later attempt validates afresh and can commit the repaired source.
				require.NoError(t, os.WriteFile(sourcePath, before, 0600))
				require.NoError(t, call())
				targetPath := e.privatePath("target")
				if op == "publish" {
					targetPath = e.sharedPath("target")
				}
				saved := layoutE2EManifest(t, targetPath)
				rec := layoutE2EWaitTerminal(t, ds, saved.SavedRevision())
				b, err := json.Marshal(saved)
				require.NoError(t, err)
				require.JSONEq(t, string(b), string(rec.Manifest))
				require.Equal(t, saved.ID, rec.Destination.WorkflowID)
			})
		}
	}
}

func TestServiceRevisionE2E_GroupSeedUsesValidatedSnapshot(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	var child, parent workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(groupEchoChildManifest), &child))
	require.NoError(t, json.Unmarshal([]byte(groupWorkflowsManifest), &parent))
	require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, &child))
	parent.Name = "sample"
	sourceDir := t.TempDir()
	manifest := filepath.Join(sourceDir, parent.Name, "workflow.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0700))
	b, err := json.Marshal(&parent)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, b, 0600))
	support := filepath.Join(filepath.Dir(manifest), "support.txt")
	require.NoError(t, os.WriteFile(support, []byte("validated supporting input"), 0600))
	locks := &groupImportBoundary{MutationCoordinator: ds.layout.mutations, before: func() {
		var changed workflow.Workflow
		require.NoError(t, json.Unmarshal(b, &changed))
		changed.Nodes[1].Config.Group.Tasks = nil
		data, err := json.Marshal(&changed)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(manifest, data, 0600))
		require.NoError(t, os.WriteFile(support, []byte("unvalidated replacement"), 0600))
	}}
	require.NoError(t, seedSampleWorkflows(ctx, sourceDir, e.workspaceDir, ds.workflowRepo, ds.publishedRepo,
		ds.layout.stamper, locks, adaptsqlite.NewWorkflowIdentityRepo(ds.db), localDefaultUser, ds.logger, ds.profileChecker))
	saved := layoutE2EManifest(t, e.sharedPath(parent.Name))
	require.Equal(t, parent.Nodes, saved.Nodes, "only the definition validated before locking may be saved")
	data, err := os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath(parent.Name)), "support.txt"))
	require.NoError(t, err)
	require.Equal(t, "validated supporting input", string(data))
	rec := layoutE2EWaitTerminal(t, ds, saved.SavedRevision())
	data, err = json.Marshal(saved)
	require.NoError(t, err)
	require.JSONEq(t, string(data), string(rec.Manifest))
	require.Equal(t, saved.ID, rec.Destination.WorkflowID)
}
