package renderreview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func artifactRenderer(t *testing.T) *BrowserRenderer {
	t.Helper()
	return newTestRenderer(t, BrowserConfig{Assets: surfaceOnly(), BrowserPath: "/no/chrome", ArtifactDir: t.TempDir()})
}

// makeCapture lays out one capture the way capture() does and returns its
// artifacts; age backdates every entry.
func makeCapture(t *testing.T, b *BrowserRenderer, rev string, attempt int, age time.Duration) []layout.Artifact {
	t.Helper()
	root := filepath.Join(b.cfg.ArtifactDir, rev, strconv.Itoa(attempt))
	require.NoError(t, os.MkdirAll(root, 0o700))
	dir, err := os.MkdirTemp(root, capturePrefix+"*")
	require.NoError(t, err)
	var out []layout.Artifact
	for _, name := range []string{"overview.png", "tile-000.png"} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("png"), 0o600))
		out = append(out, layout.Artifact{Kind: "tile", Path: p})
	}
	if age > 0 {
		old := time.Now().Add(-age)
		for _, p := range []string{out[0].Path, out[1].Path, dir, root, filepath.Dir(root)} {
			require.NoError(t, os.Chtimes(p, old, old))
		}
	}
	return out
}

// captureDirs lists every capture directory under ArtifactDir.
func captureDirs(t *testing.T, artifactDir string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(artifactDir, "*", "*", capturePrefix+"*"))
	require.NoError(t, err)
	sort.Strings(dirs)
	return dirs
}

func TestRelease_RemovesOnlyTheCitedCapture(t *testing.T) {
	b := artifactRenderer(t)
	rev := "0190a1b2-rev"
	a := makeCapture(t, b, rev, 1, 0)
	keep := makeCapture(t, b, rev, 1, 0)
	other := makeCapture(t, b, rev, 2, 0)

	require.NoError(t, b.Release(context.Background(), a))
	assert.NoDirExists(t, filepath.Dir(a[0].Path))
	assert.FileExists(t, keep[0].Path, "a sibling capture in the same attempt survives")
	assert.FileExists(t, other[0].Path)

	require.NoError(t, b.Release(context.Background(), a), "idempotent")
	require.NoError(t, b.Release(context.Background(), keep))
	assert.DirExists(t, filepath.Join(b.cfg.ArtifactDir, rev), "revision dir still holds attempt 2")
	assert.NoDirExists(t, filepath.Join(b.cfg.ArtifactDir, rev, "1"), "emptied attempt dir is pruned")
	require.NoError(t, b.Release(context.Background(), other))
	assert.NoDirExists(t, filepath.Join(b.cfg.ArtifactDir, rev), "emptied revision dir is pruned")
	assert.DirExists(t, b.cfg.ArtifactDir, "ArtifactDir itself is never removed")
}

func TestRelease_IgnoresPathsItDidNotProduce(t *testing.T) {
	b := artifactRenderer(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "r", "1", "capture-x")
	require.NoError(t, os.MkdirAll(victim, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(victim, "f.png"), nil, 0o600))
	revOnly := filepath.Join(b.cfg.ArtifactDir, "rev", "1", "overview.png") // not inside a capture dir
	require.NoError(t, os.MkdirAll(filepath.Dir(revOnly), 0o700))
	require.NoError(t, os.WriteFile(revOnly, nil, 0o600))
	link := filepath.Join(b.cfg.ArtifactDir, "rev", "2", "capture-link")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
	require.NoError(t, os.Symlink(victim, link))

	err := b.Release(context.Background(), []layout.Artifact{
		{Path: filepath.Join(victim, "f.png")},
		{Path: revOnly},
		{Path: filepath.Join(b.cfg.ArtifactDir, "..", filepath.Base(outside), "r", "1", "capture-x", "f.png")},
		{Path: "unit-only/abc.png"},
		{Path: filepath.Join(b.cfg.ArtifactDir, "rev", "1", "..", "1", "capture-x", "..", "..", "overview.png")},
		{Path: filepath.Join(link, "f.png")},
	})
	require.Error(t, err, "a symlinked capture dir is refused")
	assert.FileExists(t, filepath.Join(victim, "f.png"))
	assert.FileExists(t, revOnly)
}

func TestReconcileArtifacts_RemovesOnlyOldUnreferencedCaptures(t *testing.T) {
	b := artifactRenderer(t)
	stale := makeCapture(t, b, "rev-orphan", 1, 3*time.Hour)    // no stored result
	referenced := makeCapture(t, b, "rev-kept", 1, 3*time.Hour) // cited by a stored result
	unrefOld := makeCapture(t, b, "rev-kept", 2, 3*time.Hour)   // discarded candidate leftover
	fresh := makeCapture(t, b, "rev-live", 1, 0)                // another process is still reviewing it
	outage := makeCapture(t, b, "rev-outage", 1, 3*time.Hour)   // store unreadable: never purge
	require.NoError(t, os.WriteFile(filepath.Join(b.cfg.ArtifactDir, "README"), nil, 0o600))

	refs := func(_ context.Context, rev string) ([]layout.Artifact, error) {
		switch rev {
		case "rev-kept":
			return referenced, nil
		case "rev-outage":
			return nil, errors.New("store down")
		}
		return nil, nil
	}
	err := b.ReconcileArtifacts(context.Background(), refs)
	require.ErrorContains(t, err, "store down")

	assert.NoDirExists(t, filepath.Join(b.cfg.ArtifactDir, "rev-orphan"), "orphan revision fully pruned")
	assert.FileExists(t, referenced[0].Path)
	assert.NoFileExists(t, unrefOld[0].Path)
	assert.NoDirExists(t, filepath.Join(b.cfg.ArtifactDir, "rev-kept", "2"))
	assert.FileExists(t, fresh[0].Path, "captures within the grace period belong to live work")
	assert.FileExists(t, outage[0].Path)
	assert.NoFileExists(t, stale[0].Path)
}

// diskRenderer is the unit FakeRenderer whose artifacts are real files laid
// out exactly as BrowserRenderer.capture lays them out; Release and the
// reconcile are the production BrowserRenderer's.
type diskRenderer struct {
	*layouttest.FakeRenderer
	b *BrowserRenderer
	t *testing.T
}

func (d diskRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	out, err := d.FakeRenderer.Render(ctx, req)
	if err != nil {
		return out, err
	}
	files := makeCapture(d.t, d.b, req.Revision, req.Attempt, 0)
	for i := range out.Artifacts {
		out.Artifacts[i].Path = filepath.Join(filepath.Dir(files[0].Path), filepath.Base(out.Artifacts[i].Path))
		require.NoError(d.t, os.WriteFile(out.Artifacts[i].Path, []byte("png"), 0o600))
	}
	return out, nil
}
func (d diskRenderer) Release(ctx context.Context, a []layout.Artifact) error {
	return d.b.Release(ctx, a)
}
func (d diskRenderer) ReconcileArtifacts(ctx context.Context, r layout.ReferencedArtifacts) error {
	return d.b.ReconcileArtifacts(ctx, r)
}

// PR #85 Must 5: multi-attempt jobs through the real Coordinator and Worker
// leave only the evidence a stored, selected, current result references.
func TestArtifactLifecycle_OnlyReferencedEvidenceStaysOnDisk(t *testing.T) {
	ctx := context.Background()
	b := artifactRenderer(t)
	fake := layouttest.NewFakeRenderer()
	reviewer := layouttest.NewFakeReviewer()
	store := layouttest.NewMemStore()
	manifests := layouttest.NewFakeManifests()
	requester := layout.NewRequester(store, 4, nil)
	co, err := layout.NewCoordinator(diskRenderer{fake, b, t}, reviewer)
	require.NoError(t, err)
	w, err := layout.NewWorker(layout.WorkerConfig{Store: store, Coordinator: co, Journal: layouttest.NewFakeJournal(), Committed: layouttest.NewFakeCommittedRevisions(), Manifests: manifests, Cache: layouttest.NewFakeCache(), Requester: requester, Owner: "t"})
	require.NoError(t, err)

	scope := workflow.Scope{Kind: workflow.ScopeShared}
	save := func(retained workflow.RetainedLayout) string {
		wf := layouttest.SampleWorkflow()
		rev := uuid.Must(uuid.NewV7()).String()
		wf.Presentation = &workflow.Presentation{Revision: rev}
		require.NoError(t, requester.RequestArrangement(ctx, workflow.ArrangementRequest{Revision: rev, Scope: scope, Name: "sample", Manifest: layouttest.ManifestJSON(t, wf), Retained: retained, RequestedAt: time.Now().UTC(), Reason: workflow.SaveReasonUpdate}))
		manifests.Set(scope, "sample", rev)
		return rev
	}
	selected := func(rev string) []string {
		row, err := store.Get(ctx, rev)
		require.NoError(t, err)
		require.Positive(t, row.ResultGeneration)
		res, err := store.Result(ctx, rev, row.ResultGeneration)
		require.NoError(t, err)
		require.True(t, layout.HasRenderedEvidence(res.Geometry, res.Review), "stored Path evidence still validates")
		dir, ok := b.captureDirOf(res.Review.Evidence.Artifacts[0].Path)
		require.True(t, ok)
		return []string{dir}
	}
	blocking := func(ev string) layouttest.ReviewScript {
		return layouttest.ReviewScript{Findings: []layout.Finding{{Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{"start", "check"}, Evidence: ev}}}
	}

	// Revision A: two rejected-by-review candidates, then a review error.
	a := save(workflow.RetainedLayout{})
	reviewer.SetScript(blocking("one"), blocking("two"), layouttest.ReviewScript{Err: errors.New("review exploded")})
	require.NoError(t, w.ProcessRevision(ctx, a))
	require.Equal(t, 3, fake.RenderCalls())
	assert.Equal(t, selected(a), captureDirs(t, b.cfg.ArtifactDir), "discarded and review-error captures are released")

	// Retry A: a passing candidate replaces the published one.
	_, err = store.Retry(ctx, a, 1, time.Now())
	require.NoError(t, err)
	reviewer.SetScript()
	require.NoError(t, w.ProcessRevision(ctx, a))
	assert.Equal(t, selected(a), captureDirs(t, b.cfg.ArtifactDir), "the replaced published capture is released")

	// Revision B supersedes A: A's capture goes once B completes.
	rowA, err := store.Get(ctx, a)
	require.NoError(t, err)
	bRev := save(workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: a, FromGeneration: rowA.ResultGeneration})
	reviewer.SetScript(blocking("b1"))
	require.NoError(t, w.ProcessRevision(ctx, bRev))
	assert.Equal(t, selected(bRev), captureDirs(t, b.cfg.ArtifactDir), "superseded revision's capture is released")

	// Crash leftovers: an old orphan goes, a fresh one (live work) stays.
	orphan := makeCapture(t, b, "rev-crashed", 1, 3*time.Hour)
	live := makeCapture(t, b, bRev, 9, 0)
	require.NoError(t, w.ReconcileArtifacts(ctx))
	assert.NoFileExists(t, orphan[0].Path)
	assert.FileExists(t, live[0].Path)
	assert.Contains(t, captureDirs(t, b.cfg.ArtifactDir), selected(bRev)[0])
}

// age backdates a capture directory and its files past the reconcile grace.
func age(t *testing.T, dirs ...string) {
	t.Helper()
	old := time.Now().Add(-3 * time.Hour)
	for _, dir := range dirs {
		files, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, f := range files {
			require.NoError(t, os.Chtimes(filepath.Join(dir, f.Name()), old, old))
		}
		require.NoError(t, os.Chtimes(dir, old, old))
	}
}

// PR #85 Must 5 (verify-fix): a save that replaces every node ID retains no
// layout, so Retained.FromRevision is empty. The superseded revision's capture
// must still leave the disk, at runtime and in the startup reconcile, or
// artifacts grow without bound.
func TestArtifactLifecycle_SupersededWithoutRetainedLayoutIsReleased(t *testing.T) {
	ctx := context.Background()
	b := artifactRenderer(t)
	fake := layouttest.NewFakeRenderer()
	reviewer := layouttest.NewFakeReviewer()
	store := layouttest.NewMemStore()
	manifests := layouttest.NewFakeManifests()
	requester := layout.NewRequester(store, 4, nil)
	co, err := layout.NewCoordinator(diskRenderer{fake, b, t}, reviewer)
	require.NoError(t, err)
	w, err := layout.NewWorker(layout.WorkerConfig{Store: store, Coordinator: co, Journal: layouttest.NewFakeJournal(), Committed: layouttest.NewFakeCommittedRevisions(), Manifests: manifests, Cache: layouttest.NewFakeCache(), Requester: requester, Owner: "t"})
	require.NoError(t, err)

	scope := workflow.Scope{Kind: workflow.ScopeShared}
	save := func(retained workflow.RetainedLayout) string {
		wf := layouttest.SampleWorkflow()
		rev := uuid.Must(uuid.NewV7()).String()
		wf.Presentation = &workflow.Presentation{Revision: rev}
		require.NoError(t, requester.RequestArrangement(ctx, workflow.ArrangementRequest{Revision: rev, Scope: scope, Name: "sample", Manifest: layouttest.ManifestJSON(t, wf), Retained: retained, RequestedAt: time.Now().UTC(), Reason: workflow.SaveReasonUpdate}))
		manifests.Set(scope, "sample", rev)
		return rev
	}
	process := func(rev string) []string {
		reviewer.SetScript()
		require.NoError(t, w.ProcessRevision(ctx, rev))
		dirs, err := filepath.Glob(filepath.Join(b.cfg.ArtifactDir, rev, "*", capturePrefix+"*"))
		require.NoError(t, err)
		require.Len(t, dirs, 1, "the selected capture of %s", rev)
		return dirs
	}

	t.Run("runtime: the next completion releases it", func(t *testing.T) {
		a := process(save(workflow.RetainedLayout{}))
		age(t, a...)
		c := process(save(workflow.RetainedLayout{})) // every node ID replaced
		assert.Equal(t, c, captureDirs(t, b.cfg.ArtifactDir), "the superseded capture left the disk")
	})

	t.Run("startup: the reconcile releases it", func(t *testing.T) {
		before := captureDirs(t, b.cfg.ArtifactDir)
		age(t, before...)
		// A newer save is pending and displays nothing from the old revision.
		save(workflow.RetainedLayout{})
		require.NoError(t, w.ReconcileArtifacts(ctx))
		assert.Empty(t, captureDirs(t, b.cfg.ArtifactDir), "a superseded capture is not referenced by the pending save")

		// A pending save that retains the old layout still displays it: kept.
		kept := process(save(workflow.RetainedLayout{}))
		age(t, kept...)
		row, err := store.Get(ctx, filepath.Base(filepath.Dir(filepath.Dir(kept[0]))))
		require.NoError(t, err)
		save(workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: row.Revision, FromGeneration: row.ResultGeneration})
		require.NoError(t, w.ReconcileArtifacts(ctx))
		assert.Equal(t, kept, captureDirs(t, b.cfg.ArtifactDir), "the retained layout's capture stays while its successor is pending")
	})
}
