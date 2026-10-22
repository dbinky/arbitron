package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A decode error from the real authority must not be mistaken for an outage:
// otherwise Reader.cachedFallback hides the corruption behind valid cache bytes.
func TestLayoutReaderSQLiteCorruptionNeverUsesCache(t *testing.T) {
	for _, kind := range []string{"retained", "findings", "workflow_id", "owner_id", "geometry", "review", "preflight", "score", "attachment ownership", "outage"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t).DB
			repo := NewLayoutRepo(db)
			wf, geo, review := layouttest.CapturedGroup(t, "nested")
			rev, now := uuid.Must(uuid.NewV7()).String(), time.Now().UTC()
			retained := workflow.RetainedLayout{Source: workflow.RetainedFromManifest, Positions: map[string]workflow.NodePosition{"fan": {X: 12, Y: 34}}}
			wf.Presentation = &workflow.Presentation{Revision: rev, Scope: workflow.ScopeLabelShared, Name: "sample", SavedAt: now, Retained: retained}
			_, _, err := repo.Request(ctx, layout.NewRequest{Revision: rev, Destination: layout.Destination{Scope: workflow.Shared(), Name: "sample"}, Manifest: layouttest.ManifestJSON(t, wf), Retained: retained, AttemptLimit: 5, RequestedAt: now})
			require.NoError(t, err)
			_, err = repo.Claim(ctx, rev, "worker", now, now.Add(time.Minute))
			require.NoError(t, err)
			best := layout.StoredResult{Revision: rev, Generation: 1, Geometry: geo, Review: review, Verdict: layout.VerdictPassed, CreatedAt: now}
			require.NoError(t, repo.Complete(ctx, layout.Fence{Revision: rev, Generation: 1}, layout.Completion{State: layout.StatePassed, Result: &best, ReplaceBest: true}, now))
			cache := layouttest.NewFakeCache()
			cache.Seed(workflow.Shared(), "sample", layout.CacheEntry{Format: layout.CacheFormatV1, Revision: rev, Generation: 1, JobGeneration: 1, Contract: geo.Contract, Geometry: geo, Verdict: layout.VerdictPassed})
			reader := layout.NewReader(repo, nil, cache)
			before := reader.View(ctx, wf)
			require.True(t, before.Available)
			require.Equal(t, layout.DisplayComplete, before.DisplayStatus)
			require.Equal(t, &geo, before.Geometry)

			switch kind {
			case "outage":
				require.NoError(t, db.Close())
			case "attachment ownership":
				_, err = db.Exec(`UPDATE layout_results SET geometry=json_remove(geometry, '$.groupConnectors[0].sourceAttach.nodeId') WHERE revision=?`, rev)
			case "retained", "findings", "workflow_id", "owner_id":
				if kind == "owner_id" {
					_, err = db.Exec(`UPDATE layout_revisions SET scope_kind='private' WHERE revision=?`, rev)
					require.NoError(t, err)
				}
				_, err = db.Exec(`UPDATE layout_revisions SET `+kind+`='bad' WHERE revision=?`, rev)
			default:
				_, err = db.Exec(`UPDATE layout_results SET `+kind+`='bad' WHERE revision=?`, rev)
			}
			require.NoError(t, err)
			view := reader.View(ctx, wf)
			assert.False(t, view.Available)
			assert.True(t, view.Unreviewed)
			assert.True(t, view.RetryEligible)
			assert.Nil(t, view.Evidence)
			if kind == "outage" {
				assert.Equal(t, layout.DisplayUnavailable, view.DisplayStatus)
				assert.Equal(t, "stored status unavailable", view.FailureReason)
				assert.Equal(t, layout.ProvenanceArranged, view.Provenance)
				assert.Equal(t, &geo, view.Geometry)
			} else {
				assert.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
				assert.Contains(t, view.FailureReason, "is corrupt")
				assert.NotContains(t, view.FailureReason, "invalid character", "decode detail is logged, never served")
				assert.Equal(t, layout.ProvenanceRetainedPositions, view.Provenance)
				assert.Nil(t, view.Geometry, "valid cache cannot hide a corrupt authoritative row")
				assert.Nil(t, view.Contract)
				require.NotNil(t, view.Retained)
				assert.Equal(t, retained, *view.Retained)
			}
		})
	}
}
