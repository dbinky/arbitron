package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func preparedSeed(t *testing.T, f *commitFixture) workflow.ArrangementRequest {
	t.Helper()
	r := committedInput(t, "seed", workflow.Shared())
	r.Reason = workflow.SaveReasonSeed
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(r.Manifest, &wf))
	wf.Presentation.Reason = r.Reason
	var err error
	r.Manifest, err = json.Marshal(wf)
	require.NoError(t, err)
	require.NoError(t, f.journal.Record(context.Background(), r))
	return r
}

// Missing identity compensation must fail the release cases; name-only or
// unconditional deletion must fail the foreign, moved and surviving-tree cases.
func TestCommitSource_AbandonedSeedReleasesOnlyItsClaim(t *testing.T) {
	for _, mode := range []string{"before-tree", "partial-tree", "foreign-identity", "private-identity", "moved-identity", "surviving-tree", "publication", "unregistered", "non-seed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newCommitFixture(t, true)
			r := preparedSeed(t, f)
			entry := workflow.RegistryEntry{ID: r.WorkflowID, Name: r.Name, Scope: workflow.ScopeShared}
			if mode == "foreign-identity" {
				entry.ID = uuid.New()
			}
			if mode == "private-identity" {
				entry.Scope, entry.OwnerID = workflow.ScopePrivate, uuid.New()
			}
			if mode == "moved-identity" {
				entry.Name = "elsewhere"
			}
			if mode != "unregistered" {
				require.NoError(t, f.registry.Register(ctx, entry))
			}
			if mode == "partial-tree" {
				_, err := f.repo.PrepareTreeCommit(ctx, workflow.MutationTarget{Scope: r.Scope, Name: r.Name}, r.Revision)
				require.NoError(t, err)
				writeCommittedInput(t, f, r)
				require.NoError(t, f.repo.WriteFile(ctx, r.Scope, r.Name, "support", []byte("partial")))
			}
			if mode == "surviving-tree" {
				writeCommittedInput(t, f, committedInput(t, r.Name, r.Scope))
			}
			if mode == "publication" {
				require.NoError(t, f.pubs.CommitPublication(ctx, r.Name, workflow.Publication{Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic, Revision: uuid.Must(uuid.NewV7()).String(), WorkflowID: entry.ID}))
			}
			if mode == "non-seed" {
				require.NoError(t, f.journal.Remove(ctx, r.Revision))
				r.Reason = workflow.SaveReasonCreate
				var wf workflow.Workflow
				require.NoError(t, json.Unmarshal(r.Manifest, &wf))
				wf.Presentation.Reason = r.Reason
				var err error
				r.Manifest, err = json.Marshal(wf)
				require.NoError(t, err)
				require.NoError(t, f.journal.Record(ctx, r))
			}
			requests, err := f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			require.Empty(t, requests, "an abandoned seed must never arrange")
			_, err = f.store.Get(ctx, r.Revision)
			require.Error(t, err)
			if mode == "before-tree" || mode == "partial-tree" || mode == "unregistered" {
				_, err = f.registry.Resolve(ctx, r.Name)
				require.ErrorIs(t, err, workflow.ErrNotFound)
			} else {
				got, err := f.registry.Resolve(ctx, entry.Name)
				require.NoError(t, err)
				require.Equal(t, entry, got)
			}
			if mode == "surviving-tree" {
				_, err := f.repo.Load(ctx, r.Scope, r.Name)
				require.NoError(t, err)
			}
			requests, err = f.source.CommittedRequests(ctx)
			require.NoError(t, err, "settlement is idempotent")
			require.Empty(t, requests)
		})
	}
}

type seedRecoveryRegistry struct {
	workflow.Registry
	resolveErr, deleteErr error
}

func (r seedRecoveryRegistry) Resolve(ctx context.Context, name string) (workflow.RegistryEntry, error) {
	if r.resolveErr != nil {
		return workflow.RegistryEntry{}, r.resolveErr
	}
	return r.Registry.Resolve(ctx, name)
}
func (r seedRecoveryRegistry) Delete(ctx context.Context, id uuid.UUID) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return r.Registry.Delete(ctx, id)
}

// Losing the prepared input after a failed lookup/delete makes the claim
// unrecoverable. Retry must use the same real journal and SQLite identity.
func TestCommitSource_SeedClaimErrorRetainsRecoveryInput(t *testing.T) {
	for _, boundary := range []string{"publication", "identity-read", "identity-delete", "deadline"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			f := newCommitFixture(t, true)
			r := preparedSeed(t, f)
			entry := workflow.RegistryEntry{ID: r.WorkflowID, Name: r.Name, Scope: workflow.ScopeShared}
			require.NoError(t, f.registry.Register(ctx, entry))
			boom := errors.New("injected " + boundary)
			d := f.deps
			opCtx := ctx
			switch boundary {
			case "publication":
				d.Publications = unavailablePublications{f.pubs, boom}
			case "identity-read":
				d.Registry = seedRecoveryRegistry{Registry: f.registry, resolveErr: boom}
			case "identity-delete":
				d.Registry = seedRecoveryRegistry{Registry: f.registry, deleteErr: boom}
			case "deadline":
				var cancel context.CancelFunc
				opCtx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				cancel()
				boom = context.DeadlineExceeded
			}
			source, err := workflow.NewCommitSource(d)
			require.NoError(t, err)
			requests, err := source.CommittedRequests(opCtx)
			require.ErrorIs(t, err, boom)
			require.Empty(t, requests)
			entries, err := f.journal.Entries(ctx)
			require.NoError(t, err)
			require.Equal(t, []workflow.JournalEntry{{Request: r, State: workflow.JournalPrepared}}, entries)
			got, err := f.registry.Resolve(ctx, r.Name)
			require.NoError(t, err)
			require.Equal(t, entry, got)
			requests, err = f.source.CommittedRequests(ctx)
			require.NoError(t, err)
			require.Empty(t, requests)
			_, err = f.registry.Resolve(ctx, r.Name)
			require.ErrorIs(t, err, workflow.ErrNotFound)
		})
	}
}
