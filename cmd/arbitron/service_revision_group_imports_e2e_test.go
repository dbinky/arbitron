package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// A workflows-form import reads a different destination's child contract. The
// real coordinator must allow that validation before taking the copy lock set,
// and must still refuse a failed child lookup before preparing any new revision.
func TestServiceRevisionE2E_GroupImportsChildContractLockBoundary(t *testing.T) {
	for _, op := range []string{"publish", "copy", "duplicate", "rename"} {
		t.Run(op, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			ds, _ := e.boot(t)
			ctx, cancel := context.WithTimeout(adminCtxForLocalUser(t, ds), 10*time.Second)
			defer cancel()
			var child, parent workflow.Workflow
			require.NoError(t, json.Unmarshal([]byte(groupEchoChildManifest), &child))
			require.NoError(t, json.Unmarshal([]byte(groupWorkflowsManifest), &parent))
			require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, &child))
			require.NoError(t, ds.workflowSvc.Create(ctx, parent.Name, &parent))
			name, sourcePath := parent.Name, e.privatePath(parent.Name)
			if op == "copy" {
				require.NoError(t, ds.workflowSvc.Publish(ctx, parent.Name, "shared-parent", workflow.VisibilityPublic, ""))
				name, sourcePath = "shared-parent", e.sharedPath("shared-parent")
			}
			server := newGroupMCPServer(ds)
			call := func(dest string) {
				t.Helper()
				args := map[string]any{"name": name, "newName": dest}
				if op == "publish" {
					args = map[string]any{"name": name, "publishedName": dest}
				}
				res := callMCPTool(t, server, ctx, "workflow_"+op, args)
				require.False(t, res.IsError, mcpText(t, res))
			}
			call("valid-import")
			targetPath := e.privatePath("valid-import")
			if op == "publish" {
				targetPath = e.sharedPath("valid-import")
			}
			saved := layoutE2EManifest(t, targetPath)
			require.Equal(t, parent.Nodes, saved.Nodes)
			require.Equal(t, parent.Edges, saved.Edges)
			rec := layoutE2EWaitTerminal(t, ds, saved.SavedRevision())
			b, err := json.Marshal(saved)
			require.NoError(t, err)
			require.JSONEq(t, string(b), string(rec.Manifest))
			require.Equal(t, saved.ID, rec.Destination.WorkflowID)
			expectedRows := 1
			if op == "rename" {
				expectedRows++ // rename also relocates its source history
			}
			require.Equal(t, expectedRows, layoutE2ECountRows(t, ds.db, "valid-import"))
			if op == "rename" {
				name, sourcePath = "valid-import", targetPath
			}
			before, err := os.ReadFile(sourcePath)
			require.NoError(t, err)
			var rows int
			require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rows))
			// The child exists but its real repository decode fails; validation must
			// not misclassify this as an absent optional checker or skip it.
			require.NoError(t, os.WriteFile(e.privatePath(child.Name), []byte("{"), 0600))
			args := map[string]any{"name": name, "newName": "refused-import"}
			if op == "publish" {
				args = map[string]any{"name": name, "publishedName": "refused-import"}
			}
			res := callMCPTool(t, server, ctx, "workflow_"+op, args)
			require.True(t, res.IsError, mcpText(t, res))
			require.Contains(t, groupCodes(decodeGroupValidation(t, mcpText(t, res)).Errors), "GROUP_CHECKER_UNAVAILABLE")
			after, err := os.ReadFile(sourcePath)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.NoDirExists(t, filepath.Dir(e.privatePath("refused-import")))
			require.NoDirExists(t, filepath.Dir(e.sharedPath("refused-import")))
			require.Zero(t, registryRows(t, ds, "refused-import"))
			var afterRows int
			require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&afterRows))
			require.Equal(t, rows, afterRows)
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestServiceRevisionE2E_GroupSeedChildContractLockBoundary(t *testing.T) {
	for _, brokenChild := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "child-store-failure"}[brokenChild], func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{seedSample: "group-sample"})
			var parent workflow.Workflow
			require.NoError(t, json.Unmarshal([]byte(groupWorkflowsManifest), &parent))
			parent.Name = "group-sample"
			b, err := json.Marshal(parent)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(e.samplesDir, parent.Name, "workflow.json"), b, 0600))
			childPath := e.sharedPath("grp-echo-child")
			require.NoError(t, os.MkdirAll(filepath.Dir(childPath), 0700))
			childBytes := []byte(groupEchoChildManifest)
			if brokenChild {
				childBytes = []byte("{")
			}
			require.NoError(t, os.WriteFile(childPath, childBytes, 0600))
			ds, _ := e.boot(t)
			ledger, err := filesystem.NewSeedLedger(e.workspaceDir).Seeded(context.Background())
			require.NoError(t, err)
			if brokenChild {
				require.False(t, ledger[parent.Name])
				require.NoDirExists(t, filepath.Dir(e.sharedPath(parent.Name)))
				require.Zero(t, registryRows(t, ds, parent.Name))
				require.Zero(t, layoutE2ECountRows(t, ds.db, parent.Name))
				entries, err := ds.layout.journal.Entries(context.Background())
				require.NoError(t, err)
				require.Empty(t, entries)
				_, ok, err := ds.publishedRepo.PublicationOf(context.Background(), parent.Name)
				require.NoError(t, err)
				require.False(t, ok)
				log, err := os.ReadFile(e.logPath)
				require.NoError(t, err)
				require.Contains(t, string(log), "GROUP_CHECKER_UNAVAILABLE")
				return
			}
			saved := layoutE2EManifest(t, e.sharedPath(parent.Name))
			require.Equal(t, parent.Nodes, saved.Nodes)
			require.Equal(t, parent.Edges, saved.Edges)
			rec := layoutE2EWaitTerminal(t, ds, saved.SavedRevision())
			b, err = json.Marshal(saved)
			require.NoError(t, err)
			require.JSONEq(t, string(b), string(rec.Manifest))
			require.Equal(t, saved.ID, rec.Destination.WorkflowID)
			require.True(t, ledger[parent.Name])
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, parent.Name))
		})
	}
}
