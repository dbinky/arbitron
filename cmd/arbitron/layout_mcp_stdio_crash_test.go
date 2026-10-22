package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Instrument only the writer's durable protocol boundary; callers exercise
// surviving production readers through the shipped Cobra binary or HTTP routes.
func killLayoutPeerWriter(t *testing.T, e *layoutE2EEnv, test, key, stage string) workflow.ArrangementRequest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), key+"_CONFIG="+e.configPath, key+"_STAGE="+stage)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 20*time.Second, 10*time.Millisecond)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	b, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
	require.NoError(t, err)
	var req workflow.ArrangementRequest
	require.NoError(t, json.Unmarshal(b, &req))
	return req
}

func mcpStdioRead(t *testing.T, p *layoutStdioProcess, tool, name string) *workflow.Workflow {
	t.Helper()
	args := map[string]any{"name": name}
	if tool == "workflow_read_file" {
		args["path"] = "workflow.json"
	}
	res := p.call(t, "read", tool, args)
	require.False(t, res.IsError, mcpE2EText(t, res))
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &wf))
	if tool == "workflow_get" {
		var out struct {
			Arrangement layout.View `json:"arrangement"`
		}
		require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &out))
		require.Equal(t, wf.SavedRevision(), out.Arrangement.SavedRevision)
	}
	return &wf
}

func mcpStdioRetry(t *testing.T, p *layoutStdioProcess, ds *domainStack, name, revision string) layout.Record {
	t.Helper()
	res := p.call(t, "retry", "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": revision})
	require.False(t, res.IsError, mcpE2EText(t, res))
	var ack layout.RetryAck
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &ack))
	require.Equal(t, revision, ack.SavedRevision)
	require.Equal(t, layout.StatePending, ack.State)
	require.Equal(t, layout.DisplayPending, ack.DisplayStatus)
	return stdioTerminal(t, ds.db, revision, ack.JobGeneration)
}

func mcpStdioUpdate(t *testing.T, p *layoutStdioProcess, e *layoutE2EEnv, ds *domainStack, name string, shared bool) *workflow.Workflow {
	t.Helper()
	wf := layoutE2EWorkflow(name, "Surviving stdio save 并行 🚀")
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	res := p.call(t, "save", "workflow_update", map[string]any{"name": name, "manifest": string(b)})
	require.False(t, res.IsError, mcpE2EText(t, res))
	path, scope := e.privatePath(name), workflow.Private(localDefaultUser)
	if shared {
		path, scope = e.sharedPath(name), workflow.Shared()
	}
	saved := layoutE2EManifest(t, path)
	row := stdioTerminal(t, ds.db, saved.SavedRevision(), 1)
	b, err = json.Marshal(saved)
	require.NoError(t, err)
	require.JSONEq(t, string(b), string(row.Manifest))
	require.Equal(t, wf.Nodes, saved.Nodes)
	require.Equal(t, wf.Edges, saved.Edges)
	require.Equal(t, layout.Destination{Scope: scope, Name: name, WorkflowID: saved.ID}, row.Destination)
	ctx := adminCtxForLocalUser(t, ds)
	resolved, err := adaptsqlite.NewWorkflowIdentityRepo(ds.db).ResolveName(ctx, saved.ID)
	require.NoError(t, err)
	require.Equal(t, name, resolved)
	if shared {
		proof, ok, err := ds.publishedRepo.PublicationOf(ctx, name)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, saved.SavedRevision(), proof.Revision)
		require.Equal(t, saved.ID, proof.WorkflowID)
	}
	return saved
}

func TestLayoutMCPStdio_SharedPeerCrash(t *testing.T) {
	binary := buildLayoutStdioBinary(t)
	for _, boundary := range []string{"before-proof", "after-proof"} {
		for _, first := range []string{"workflow_read_file", "workflow_get", "workflow_update", "workflow_layout_retry"} {
			t.Run(boundary+"/"+first, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
				ds, _ := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Prior committed graph")))
				require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
				prior := layoutE2EManifest(t, e.sharedPath("target"))
				layoutE2EWaitTerminal(t, ds, prior.SavedRevision())
				ds.layout.worker.Stop()
				p := startLayoutStdio(t, binary, e)
				p.initialize(t) // boot/recovery finished BEFORE the writer's commit window
				candidate := killLayoutPeerWriter(t, e, "TestLayoutE2E_CrashProcess", "ARBITRON_LAYOUT_CRASH", "shared/"+boundary)
				repo := ds.workflowRepo.(*filesystem.WorkflowRepo)
				pending, err := repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.NotEmpty(t, pending, "first tool must encounter durable unsettled evidence")
				want := prior
				if boundary == "after-proof" {
					want = &workflow.Workflow{}
					require.NoError(t, json.Unmarshal(candidate.Manifest, want))
				}
				switch first {
				case "workflow_read_file", "workflow_get":
					require.Equal(t, *want, *mcpStdioRead(t, p, first, "target"))
				case "workflow_update":
					saved := mcpStdioUpdate(t, p, e, ds, "target", true)
					require.NotEqual(t, candidate.Revision, saved.SavedRevision())
				case "workflow_layout_retry":
					// Missing-row post-proof input is recovered by the shared retry seam.
					row := mcpStdioRetry(t, p, ds, "target", want.SavedRevision())
					b, err := json.Marshal(want)
					require.NoError(t, err)
					require.JSONEq(t, string(b), string(row.Manifest))
				}
				pending, err = repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.Empty(t, pending, "successful tool must settle, not merely read visible bytes")
				if first != "workflow_update" {
					require.Equal(t, *want, *mcpStdioRead(t, p, "workflow_get", "target"))
					require.Equal(t, *want, *layoutE2EManifest(t, e.sharedPath("target")))
				}
				proof, ok, err := ds.publishedRepo.PublicationOf(ctx, "target")
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, layoutE2ESavedRevision(t, e.sharedPath("target")), proof.Revision)
				// Replay the actual settled journal input; it must retain the exact
				// committed candidate even if a first-call save has superseded it.
				requests, err := ds.layout.commits.CommittedRequests(ctx)
				require.NoError(t, err)
				for _, request := range requests {
					require.NoError(t, ds.layout.requester.RequestArrangement(ctx, request))
				}
				if boundary == "after-proof" {
					row, err := ds.layout.store.Get(ctx, candidate.Revision)
					require.NoError(t, err)
					require.Equal(t, candidate.Manifest, row.Manifest)
					require.Equal(t, layout.Destination{Scope: candidate.Scope, Name: candidate.Name, WorkflowID: candidate.WorkflowID}, row.Destination)
				}
				if boundary == "before-proof" {
					_, err = ds.layout.store.Get(ctx, candidate.Revision)
					require.ErrorIs(t, err, layout.ErrRecordNotFound)
				}
				final := mcpStdioUpdate(t, p, e, ds, "target", true)
				require.Equal(t, prior.ID, final.ID)
				before := stdioTerminal(t, ds.db, final.SavedRevision(), 1)
				retried := mcpStdioRetry(t, p, ds, "target", final.SavedRevision())
				require.Equal(t, before.JobGeneration+1, retried.JobGeneration)
				require.Equal(t, before.Manifest, retried.Manifest)
				p.finish(t, true)
			})
		}
	}
}

func TestLayoutMCPStdio_RenamePeerCrash(t *testing.T) {
	binary := buildLayoutStdioBinary(t)
	for _, kind := range []string{"private", "shared"} {
		for _, boundary := range []string{"before-identity", "after-identity"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
				ds, _ := e.boot(t)
				ds.layout.worker.Stop()
				ctx := adminCtxForLocalUser(t, ds)
				p := startLayoutStdio(t, binary, e)
				p.initialize(t)
				candidate := killLayoutPeerWriter(t, e, "TestCommitProtocol_RenameProcess", "ARBITRON_RENAME_CRASH", kind+"/"+boundary)
				visiblePath := e.privatePath("renamed")
				source, scope := "original", workflow.Private(localDefaultUser)
				if kind == "shared" {
					visiblePath, source, scope = e.sharedPath("renamed"), "published", workflow.Shared()
				}
				visible, err := os.ReadFile(visiblePath)
				require.NoError(t, err)
				require.Equal(t, candidate.Revision, layoutE2ESavedRevision(t, visiblePath))
				// Raw bytes MUST be the first read, before any manifest/query can settle.
				for _, tool := range []string{"workflow_read_file", "workflow_get", "workflow_update", "workflow_layout_retry"} {
					args := map[string]any{"name": "renamed", "path": "workflow.json", "expectedRevision": candidate.Revision,
						"manifest": queryE2EJSON(t, layoutE2EWorkflow("renamed", "Must not commit"))}
					res := p.call(t, tool, tool, args)
					require.True(t, res.IsError, tool)
					require.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", mcpE2EText(t, res), tool)
				}
				after, err := os.ReadFile(visiblePath)
				require.NoError(t, err)
				require.Equal(t, visible, after)
				_, err = ds.layout.store.Get(ctx, candidate.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound)
				// Recovery uses the real registry authority and both endpoint locks.
				requests, err := ds.layout.commits.CommittedRequests(ctx)
				require.NoError(t, err)
				for _, req := range requests {
					require.NoError(t, ds.layout.requester.RequestArrangement(ctx, req))
				}
				prior, err := os.ReadFile(filepath.Join(e.root, "prior-manifest.json"))
				require.NoError(t, err)
				winner, loser, expected := source, "renamed", prior
				if boundary == "after-identity" {
					winner, loser, expected = "renamed", source, candidate.Manifest
				}
				var want workflow.Workflow
				require.NoError(t, json.Unmarshal(expected, &want))
				registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
				resolved, err := registry.ResolveName(ctx, want.ID)
				require.NoError(t, err)
				require.Equal(t, winner, resolved)
				for _, tool := range []string{"workflow_read_file", "workflow_get"} {
					require.Equal(t, want, *mcpStdioRead(t, p, tool, winner))
					res := p.call(t, "loser", tool, map[string]any{"name": loser, "path": "workflow.json"})
					require.True(t, res.IsError)
				}
				if kind == "shared" {
					proof, ok, err := ds.publishedRepo.PublicationOf(ctx, winner)
					require.NoError(t, err)
					require.True(t, ok)
					require.Equal(t, want.SavedRevision(), proof.Revision)
				}
				if boundary == "after-identity" {
					row, err := ds.layout.store.Get(ctx, candidate.Revision)
					require.NoError(t, err)
					require.Equal(t, candidate.Manifest, row.Manifest)
					require.Equal(t, layout.Destination{Scope: scope, Name: winner, WorkflowID: want.ID}, row.Destination)
				} else {
					_, err := ds.layout.store.Get(ctx, candidate.Revision)
					require.ErrorIs(t, err, layout.ErrRecordNotFound)
				}
				final := mcpStdioUpdate(t, p, e, ds, winner, kind == "shared")
				require.Equal(t, want.ID, final.ID)
				before := stdioTerminal(t, ds.db, final.SavedRevision(), 1)
				retried := mcpStdioRetry(t, p, ds, winner, final.SavedRevision())
				require.Equal(t, before.JobGeneration+1, retried.JobGeneration)
				require.Equal(t, before.Manifest, retried.Manifest)
				p.finish(t, true)
			})
		}
	}
}
