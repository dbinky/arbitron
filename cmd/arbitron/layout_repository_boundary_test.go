package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Read through the production services, including the persisted Engine snapshot,
// rather than treating a successful repository Load as proof of every consumer.
func layoutRepositoryRead(ctx context.Context, ds *domainStack, consumer string) ([]byte, error) {
	switch consumer {
	case "load":
		wf, err := ds.workflowSvc.Load(ctx, "target")
		if err != nil {
			return nil, err
		}
		return json.Marshal(wf)
	case "trigger":
		id, _, err := ds.eng.Trigger(ctx, "target", nil, "normal")
		if err != nil {
			return nil, err
		}
		inst, err := ds.instanceRepo.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(inst.WorkflowSnapshot)
	case "catalog":
		summaries, err := ds.workflowSvc.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, summary := range summaries {
			if summary.Name == "target" {
				return json.Marshal(summary)
			}
		}
		return nil, fmt.Errorf("target missing from successful catalog")
	default:
		return nil, fmt.Errorf("unknown repository consumer %q", consumer)
	}
}

func layoutRepositoryInstanceCount(t *testing.T, ds *domainStack) int {
	t.Helper()
	var count int
	require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM instances WHERE workflow_name = ?", "target").Scan(&count))
	return count
}

func TestLayoutRepository_StagedPublicationReadBoundaries(t *testing.T) {
	for _, consumer := range []string{"load", "trigger", "catalog"} {
		for _, boundary := range []string{"before-proof", "after-proof"} {
			t.Run(consumer+"/"+boundary, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
				ds, _ := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				original := layoutE2EWorkflow("source", "Old committed label")
				original.Description = "Old committed catalog description"
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", original))
				require.NoError(t, ds.workflowRepo.WriteFile(ctx, workflow.Private(localDefaultUser), "source", "support.txt", []byte("old support")))
				require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
				prior, err := os.ReadFile(e.sharedPath("target"))
				require.NoError(t, err)
				oldRevision := layoutE2ESavedRevision(t, e.sharedPath("target"))
				layoutE2EWaitTerminal(t, ds, oldRevision)
				// No recovery poll may settle the killed peer before the consumer under test.
				ds.layout.worker.Stop()

				processCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestLayoutE2E_CrashProcess$")
				cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_CRASH_CONFIG="+e.configPath, "ARBITRON_LAYOUT_CRASH_STAGE=shared/"+boundary)
				cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
				require.NoError(t, cmd.Start())
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				require.Eventually(t, func() bool {
					_, err := os.Stat(filepath.Join(e.root, "crash-ready"))
					return err == nil
				}, 10*time.Second, 10*time.Millisecond)
				var prepared workflow.ArrangementRequest
				captured, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(captured, &prepared))
				require.NotEqual(t, oldRevision, prepared.Revision)
				require.Equal(t, prepared.Revision, layoutE2ESavedRevision(t, e.sharedPath("target")), "new tree must actually be visible at the crash boundary")
				require.NotEqual(t, layoutE2ESavedRevision(t, e.privatePath("source")), prepared.Revision, "published target must never inherit the source token")

				// The writer is still alive and owns the operation locks. Every real
				// consumer must wait, even though a complete candidate is on disk.
				readCtx, cancelRead := context.WithTimeout(ctx, 150*time.Millisecond)
				got, err := layoutRepositoryRead(readCtx, ds, consumer)
				cancelRead()
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Empty(t, got)
				require.Zero(t, layoutRepositoryInstanceCount(t, ds), "blocked trigger must not persist an intermediate snapshot")

				require.NoError(t, cmd.Process.Kill())
				require.Error(t, cmd.Wait())
				// This is the already-running peer's FIRST post-crash read. It must
				// restore the old tree or accept the exact committed publication.
				want := prior
				wantSupport := "old support"
				if boundary == "after-proof" {
					want, wantSupport = prepared.Manifest, "new support"
				}
				got, err = layoutRepositoryRead(processCtx, ds, consumer)
				require.NoError(t, err)
				if consumer == "catalog" {
					var wf workflow.Workflow
					require.NoError(t, json.Unmarshal(want, &wf))
					summary, err := json.Marshal(workflow.WorkflowSummary{
						Name: wf.Name, Description: wf.Description, Author: wf.Author, NodeCount: len(wf.Nodes),
						Scope: workflow.ScopeLabelShared, Publisher: localDefaultUser.String(), Visibility: string(workflow.VisibilityPublic),
					})
					require.NoError(t, err)
					require.JSONEq(t, string(summary), string(got), "catalog must not leak an uncommitted summary or silently omit the target")
				} else {
					require.JSONEq(t, string(want), string(got), "complete graph, identity, revision and retained presentation must reach the actual sink")
				}
				onDisk, err := os.ReadFile(e.sharedPath("target"))
				require.NoError(t, err)
				require.JSONEq(t, string(want), string(onDisk))
				if boundary == "before-proof" {
					require.Equal(t, prior, onDisk, "rollback must preserve every original manifest byte")
				}
				support, err := os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath("target")), "support.txt"))
				require.NoError(t, err)
				require.Equal(t, wantSupport, string(support))
				if consumer == "trigger" {
					require.Equal(t, 1, layoutRepositoryInstanceCount(t, ds))
				} else {
					require.Zero(t, layoutRepositoryInstanceCount(t, ds))
				}
			})
		}
	}
}

func TestLayoutRepository_UnresolvedRelocationFailsClosed(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "target", layoutE2EWorkflow("target", "Committed input")))
	saved, err := os.ReadFile(e.privatePath("target"))
	require.NoError(t, err)
	wf := layoutE2EManifest(t, e.privatePath("target"))
	layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	ds.layout.worker.Stop()
	intent, err := ds.layout.journal.BeginRelocation(ctx, workflow.Private(localDefaultUser), "target", workflow.Private(localDefaultUser), "destination", wf.ID)
	require.NoError(t, err)
	for _, consumer := range []string{"load", "trigger", "catalog"} {
		t.Run(consumer, func(t *testing.T) {
			got, err := layoutRepositoryRead(ctx, ds, consumer)
			require.ErrorIs(t, err, workflow.ErrMutationPending)
			require.Empty(t, got, "undecided relocation must not become a successful partial read")
		})
	}
	require.Zero(t, layoutRepositoryInstanceCount(t, ds))
	after, err := os.ReadFile(e.privatePath("target"))
	require.NoError(t, err)
	require.Equal(t, saved, after)
	intents, err := ds.layout.journal.RelocationIntents(ctx)
	require.NoError(t, err)
	require.Contains(t, intents, intent, "a read cannot decide a relocation without both endpoint locks")
}
