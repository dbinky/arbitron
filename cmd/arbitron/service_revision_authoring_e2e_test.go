package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The identity transaction is the move's commit point. A rejected transaction
// must leave neither a target tree nor a captured target revision in SQLite.
func TestServiceRevisionE2E_MoveRegistryRollback(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, op := range []string{"rename", "duplicate", "transfer"} {
			if shared && op == "transfer" { // shared transfer changes attribution only
				continue
			}
			t.Run(map[bool]string{false: "private/", true: "shared/"}[shared]+op, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{})
				ds, _ := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Rollback input")))
				name, path := "source", e.privatePath("source")
				if shared {
					require.NoError(t, ds.workflowSvc.Publish(ctx, name, "public", workflow.VisibilityPublic, ""))
					name, path = "public", e.sharedPath("public")
				}
				saved := layoutE2EManifest(t, path)
				layoutE2EWaitTerminal(t, ds, saved.SavedRevision())
				support := filepath.Join(filepath.Dir(path), "keep.txt")
				require.NoError(t, os.WriteFile(support, []byte("unchanged supporting bytes\n"), 0600))
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				var rows int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rows))
				// This is the actual identity repository's SQL failure, not a service fake.
				_, err = ds.db.Exec(`CREATE TRIGGER reject_identity_insert BEFORE INSERT ON workflow_identity BEGIN SELECT RAISE(ABORT, 'injected identity failure'); END`)
				require.NoError(t, err)
				_, err = ds.db.Exec(`CREATE TRIGGER reject_identity_update BEFORE UPDATE ON workflow_identity BEGIN SELECT RAISE(ABORT, 'injected identity failure'); END`)
				require.NoError(t, err)
				target := e.privatePath("target")
				switch op {
				case "rename":
					if shared {
						target = e.sharedPath("target")
					}
					err = ds.workflowSvc.Rename(ctx, name, "target")
				case "duplicate":
					err = ds.workflowSvc.Duplicate(ctx, name, "target")
				case "transfer":
					owner := uuid.New()
					target = filepath.Join(e.workspaceDir, "users", owner.String(), name, "workflow.json")
					err = ds.workflowSvc.TransferOwnership(ctx, name, owner)
				}
				require.ErrorContains(t, err, "injected identity failure")
				require.NoDirExists(t, filepath.Dir(target))
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, before, after)
				b, err := os.ReadFile(support)
				require.NoError(t, err)
				require.Equal(t, "unchanged supporting bytes\n", string(b))
				var afterRows int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&afterRows))
				require.Equal(t, rows, afterRows, "failed identity commit cannot arrange a target")
				entries, err := ds.layout.journal.Entries(ctx)
				require.NoError(t, err)
				require.Empty(t, entries, "rolled-back input must not be recoverable as a job")
				intents, err := ds.layout.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Empty(t, intents)
				loaded, err := ds.workflowSvc.Load(ctx, name)
				require.NoError(t, err)
				require.Equal(t, saved, loaded)
				if shared {
					proof, ok, err := ds.publishedRepo.PublicationOf(ctx, name)
					require.NoError(t, err)
					require.True(t, ok)
					require.Equal(t, saved.SavedRevision(), proof.Revision)
					require.Equal(t, saved.ID, proof.WorkflowID)
				}
			})
		}
	}
}

func TestServiceRevisionE2E_MoveCollisionAndCancellation(t *testing.T) {
	for _, op := range []string{"rename", "duplicate", "transfer"} {
		t.Run(op, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{})
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Original")))
			require.NoError(t, ds.workflowSvc.Create(ctx, "occupied", layoutE2EWorkflow("occupied", "Collision")))
			source := layoutE2EManifest(t, e.privatePath("source"))
			occupied := layoutE2EManifest(t, e.privatePath("occupied"))
			layoutE2EWaitTerminal(t, ds, source.SavedRevision())
			layoutE2EWaitTerminal(t, ds, occupied.SavedRevision())
			owner := uuid.New()
			target := filepath.Join(e.workspaceDir, "users", owner.String(), "source", "workflow.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0700))
			b, err := json.Marshal(occupied)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(target, b, 0600))
			move := func(ctx context.Context, name string) error {
				switch op {
				case "rename":
					return ds.workflowSvc.Rename(ctx, "source", name)
				case "duplicate":
					return ds.workflowSvc.Duplicate(ctx, "source", name)
				default:
					return ds.workflowSvc.TransferOwnership(ctx, "source", owner)
				}
			}
			require.ErrorIs(t, move(ctx, "occupied"), workflow.ErrAlreadyExists)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			require.ErrorIs(t, move(cancelled, "unused"), context.Canceled)
			require.Equal(t, source, layoutE2EManifest(t, e.privatePath("source")))
			require.Equal(t, occupied, layoutE2EManifest(t, e.privatePath("occupied")))
			require.Equal(t, occupied, layoutE2EManifest(t, target))
			require.NoDirExists(t, filepath.Dir(e.privatePath("unused")))
			var rows int
			require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rows))
			require.Equal(t, 2, rows)
		})
	}
}
