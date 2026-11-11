package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// CandidateInventory reads the routes of a prospective profile without mutation.
type CandidateInventory interface {
	provider.ProfileConnectionSource
	PathsForProfile(context.Context, provider.Profile) (PathSnapshot, error)
	VersionRequests(context.Context, provider.Profile) ([]modelidentity.VersionRequest, error)
}

type CandidateDeps struct {
	Store       CatalogStore
	Inventory   CandidateInventory
	Pipeline    *DiscoveryPipeline
	Coordinator *RefreshCoordinator
	Gate        Gate
	Mapper      EntryMapper
	Clock       func() time.Time
}

// CandidateService shares refresh admission and durable observations with the
// installation scan. Only persisted exact-route proof authorizes selection.
type CandidateService struct{ d CandidateDeps }

func NewCandidateService(d CandidateDeps) (*CandidateService, error) {
	if d.Store == nil || d.Inventory == nil || d.Pipeline == nil || d.Coordinator == nil || d.Gate == nil || d.Mapper == nil {
		return nil, errNilCollaborator
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &CandidateService{d: d}, nil
}

type prospect struct {
	view   CatalogView
	snap   PathSnapshot
	paths  []ExecutionPath
	target provider.FollowTarget
}

func (c *CandidateService) read(ctx context.Context, p provider.Profile) (prospect, error) {
	if err := ctx.Err(); err != nil {
		return prospect{}, err
	}
	if p.SelectionMode != provider.SelectionFollowing || p.FollowTarget == nil {
		return prospect{}, fmt.Errorf("%w: a following profile needs a follow target", provider.ErrInvalidSelection)
	}
	view, err := c.d.Store.Snapshot(ctx)
	if err != nil {
		return prospect{}, fmt.Errorf("reading stored catalog: %w", err)
	}
	snap, err := c.d.Inventory.PathsForProfile(ctx, p.Clone())
	if err != nil {
		return prospect{}, err
	}
	paths := consumingPaths(snap, p.Alias)
	if len(paths) == 0 || snap.Revision == "" || snap.ExecutionRevision.ID != snap.Revision {
		return prospect{}, fmt.Errorf("%w: no coherent execution routes for %q", provider.ErrExecutionRevisionUnavailable, p.Alias)
	}
	// Re-map retained raw metadata, not the classifications frozen by a past scan.
	view, err = remapView(ctx, c.d.Mapper, view)
	if err != nil {
		return prospect{}, err
	}
	for _, e := range knownEntries(view.Catalog, view) {
		if e.PolicyRevision != snap.ExecutionRevision.PolicyRevision {
			return prospect{}, provider.ErrExecutionRevisionStale
		}
	}
	return prospect{view: view, snap: snap, paths: paths, target: *p.FollowTarget}, nil
}

func remapView(ctx context.Context, mapper EntryMapper, view CatalogView) (CatalogView, error) {
	view.Catalog.Slices = slices.Clone(view.Catalog.Slices)
	for i, sl := range view.Catalog.Slices {
		if len(sl.Models) == 0 {
			continue
		}
		models := make([]modelidentity.CatalogModel, 0, len(sl.Models))
		entries := make([]modelidentity.Entry, 0, len(sl.Models))
		for _, cm := range sl.Models {
			e, err := mapper.Map(ctx, cm)
			if errors.Is(err, modelidentity.ErrUnsupportedNativeID) {
				// Persisted before the native-id allow-list; never a candidate.
				continue
			}
			if err != nil {
				return CatalogView{}, err
			}
			models, entries = append(models, cm), append(entries, e)
		}
		view.Catalog.Slices[i].Models, view.Catalog.Slices[i].Entries = models, entries
	}
	if len(view.HistoricalModels) > 0 {
		view.HistoricalEntries = nil
		historical := view.HistoricalModels
		view.HistoricalModels = nil
		for _, cm := range historical {
			e, err := mapper.Map(ctx, cm)
			if errors.Is(err, modelidentity.ErrUnsupportedNativeID) {
				continue
			}
			if err != nil {
				return CatalogView{}, err
			}
			view.HistoricalModels = append(view.HistoricalModels, cm)
			view.HistoricalEntries = append(view.HistoricalEntries, e)
		}
	}
	return view, nil
}

func (c *CandidateService) Newest(ctx context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	return c.selectStart(ctx, p, false)
}

func (c *CandidateService) ValidateStart(ctx context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	if p.Model == "" {
		return provider.EligibleCandidate{}, fmt.Errorf("%w: a model is required", provider.ErrInvalidSelection)
	}
	return c.selectStart(ctx, p, true)
}

func (c *CandidateService) selectStart(ctx context.Context, p provider.Profile, explicit bool) (provider.EligibleCandidate, error) {
	p = p.Clone()
	pr, err := c.read(ctx, p)
	if err != nil {
		return provider.EligibleCandidate{}, err
	}
	if cand, err := selectPersisted(pr, p.Model, explicit); err == nil {
		return cand, nil
	} else if errors.Is(err, provider.ErrInvalidSelection) {
		return provider.EligibleCandidate{}, err
	}
	// The coordinator owns network work, including cold creation, and no nested
	// profile mutation is allowed while collecting or committing these inputs.
	err = c.d.Coordinator.Do(ctx, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, DefaultCycleBudget)
		defer cancel()
		commit, err := c.d.Pipeline.Collect(ctx, &p)
		if err != nil {
			return err
		}
		if len(commit.Advances) != 0 {
			return fmt.Errorf("modelcatalog: bootstrap proposed a profile mutation")
		}
		commit.Report.At = c.d.Clock()
		return c.d.Gate.WithRead(ctx, func(ctx context.Context) error {
			current, err := c.d.Inventory.PathsForProfile(ctx, p.Clone())
			if err != nil {
				return err
			}
			if current.Revision != pr.snap.Revision || !reflect.DeepEqual(current.ExecutionRevision, pr.snap.ExecutionRevision) {
				return provider.ErrExecutionRevisionStale
			}
			report, err := c.d.Store.CommitCycle(ctx, commit)
			if err != nil {
				return err
			}
			return c.d.Store.MarkPublished(ctx, report.ID)
		})
	})
	if err != nil {
		return provider.EligibleCandidate{}, err
	}
	pr, err = c.read(ctx, p)
	if err != nil {
		return provider.EligibleCandidate{}, err
	}
	return selectPersisted(pr, p.Model, explicit)
}

func selectPersisted(pr prospect, model string, explicit bool) (provider.EligibleCandidate, error) {
	entries := newestEntries(pr)
	if explicit {
		e, err := startEntry(pr, model)
		if err != nil {
			return provider.EligibleCandidate{}, err
		}
		entries = []modelidentity.Entry{e}
	}
	for _, e := range entries {
		if persistedProof(pr, e) {
			return candidateFor(pr, e), nil
		}
	}
	return provider.EligibleCandidate{}, fmt.Errorf("%w for %s/%s", provider.ErrNoEligibleCandidate, pr.target.Provider, pr.target.Tier)
}

func persistedProof(pr prospect, e modelidentity.Entry) bool {
	if !followable(pr, e) || len(pr.paths) == 0 {
		return false
	}
	for _, path := range pr.paths {
		found := false
		for _, ev := range pr.view.Evidence {
			if evidenceMatches(ev, path, e) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ConnectionProvider forwards the metadata-only policy seam to the inventory.
func (c *CandidateService) ConnectionProvider(ctx context.Context, p provider.Profile) (string, error) {
	return c.d.Inventory.ConnectionProvider(ctx, p.Clone())
}

func (c *CandidateService) VersionRequests(ctx context.Context, p provider.Profile) ([]modelidentity.VersionRequest, error) {
	reqs, err := c.d.Inventory.VersionRequests(ctx, p.Clone())
	if err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, fmt.Errorf("%w: no prospective routes for %q", provider.ErrExecutionRevisionUnavailable, p.Alias)
	}
	return reqs, nil
}

// RevalidateStart runs under the caller's mutation lease. It does no network,
// coordinator admission or recursive gate acquisition.
func (c *CandidateService) RevalidateStart(ctx context.Context, p provider.Profile, cand provider.EligibleCandidate) error {
	pr, err := c.read(ctx, p)
	if err != nil {
		return err
	}
	if p.Model == "" || p.Model != cand.Model || cand.CanonicalID == "" {
		return fmt.Errorf("%w: candidate differs from the profile model", provider.ErrInvalidSelection)
	}
	if pr.snap.Revision != cand.PathRevision || !reflect.DeepEqual(pr.snap.ExecutionRevision, cand.ExecutionRevision) {
		return provider.ErrExecutionRevisionStale
	}
	e, err := startEntry(pr, p.Model)
	if err != nil {
		return err
	}
	if e.CanonicalID != cand.CanonicalID || !persistedProof(pr, e) {
		return fmt.Errorf("%w: no persisted exact-route proof", provider.ErrExecutionRevisionUnavailable)
	}
	return nil
}

func candidateFor(pr prospect, e modelidentity.Entry) provider.EligibleCandidate {
	return provider.EligibleCandidate{Model: e.SpecificModel, CanonicalID: e.CanonicalID, PathRevision: pr.snap.Revision, ExecutionRevision: pr.snap.ExecutionRevision.Clone()}
}

func followable(pr prospect, e modelidentity.Entry) bool {
	return e.Provider == pr.target.Provider && e.Tier != "" && e.Tier == pr.target.Tier && !e.Ambiguous && e.Immutable && e.SpecificModel != "" && e.OrderKey != "" && e.PolicyRevision == pr.snap.ExecutionRevision.PolicyRevision
}

// Selection examines every eligible release; only the scan work is budgeted.
func newestEntries(pr prospect) []modelidentity.Entry {
	var out []modelidentity.Entry
	for _, sl := range pr.view.Catalog.Slices {
		if sl.Provider != pr.target.Provider || sl.Status != SliceStatusOK {
			continue
		}
		for _, e := range sl.Entries {
			if followable(pr, e) {
				out = append(out, e)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b modelidentity.Entry) int {
		if c, err := modelidentity.Compare(b, a); err == nil && c != 0 {
			return c
		}
		if a.SpecificModel < b.SpecificModel {
			return -1
		}
		if a.SpecificModel > b.SpecificModel {
			return 1
		}
		return 0
	})
	return out
}

func startEntry(pr prospect, model string) (modelidentity.Entry, error) {
	if model == "" {
		return modelidentity.Entry{}, fmt.Errorf("%w: a model is required", provider.ErrInvalidSelection)
	}
	var pool []modelidentity.Entry
	for _, sl := range pr.view.Catalog.Slices {
		if sl.Provider == pr.target.Provider && sl.Status == SliceStatusOK {
			pool = append(pool, sl.Entries...)
		}
	}
	pool = append(pool, pr.view.HistoricalEntries...)
	for _, e := range pool {
		if e.Provider != pr.target.Provider || (e.SpecificModel != model && e.NativeID != model) || e.PolicyRevision != pr.snap.ExecutionRevision.PolicyRevision || e.Tier != pr.target.Tier {
			continue
		}
		if e.SpecificModel != model {
			return modelidentity.Entry{}, fmt.Errorf("%w: %q is not a fixed-version spelling (use %q)", provider.ErrInvalidSelection, model, e.SpecificModel)
		}
		if followable(pr, e) {
			return e, nil
		}
	}
	return modelidentity.Entry{}, fmt.Errorf("%w: %q is not a cataloged %s/%s model", provider.ErrNoEligibleCandidate, model, pr.target.Provider, pr.target.Tier)
}
