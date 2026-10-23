package modelcatalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/settings"
)

// DefaultCycleBudget bounds recovery, collection, commit, publication and drain.
const DefaultCycleBudget = 3 * time.Hour

// CommitReserve keeps collection from consuming publication's live context.
const CommitReserve = 30 * time.Second

// ErrRefreshClosed is returned once the coordinator has been closed.
var ErrRefreshClosed = errors.New("modelcatalog: refresh coordinator closed")

// Collaborator ports of the pipeline. Each is satisfied by a production type
// at the composition root; none may be nil.
type (
	// SettingsReader returns the raw catalog URL and webhook (never redacted).
	SettingsReader = settings.InternalReader
	ProfileLister  interface {
		ListInternal(context.Context) ([]provider.Profile, error)
	}
	// AdvanceValidator checks unchanged capabilities and route versions without mutation.
	AdvanceValidator interface {
		ValidateAdvance(context.Context, provider.AdvanceRequest) error
	}
	PathInventory interface {
		Snapshot(context.Context) (PathSnapshot, error)
	}
	// EntryMapper classifies one catalog row under the persisted D2 policy.
	EntryMapper interface {
		Map(context.Context, modelidentity.CatalogModel) (modelidentity.Entry, error)
	}
)

// DiscoveryDeps are the pipeline's collaborators. Zero Clock, NewID and Budget
// take production values.
type DiscoveryDeps struct {
	Settings         SettingsReader
	Fetcher          CatalogFetcher
	Normalizer       CatalogNormalizer
	Mapper           EntryMapper
	Inventory        PathInventory
	Profiles         ProfileLister
	Store            CatalogStore
	Probe            UsabilityProbe
	AdvanceValidator AdvanceValidator
	Clock            func() time.Time
	NewID            func() string
	Budget           time.Duration
	Logger           *slog.Logger
}

// DiscoveryPipeline turns the live world (settings, catalog source, persisted
// policy, execution paths, usability probes) into one CycleCommit.
type DiscoveryPipeline struct {
	d        DiscoveryDeps
	selector *AdvanceSelector
}

func NewDiscoveryPipeline(d DiscoveryDeps) (*DiscoveryPipeline, error) {
	if d.Settings == nil || d.Fetcher == nil || d.Normalizer == nil || d.Mapper == nil || d.Inventory == nil || d.Profiles == nil || d.Store == nil || d.Probe == nil || d.AdvanceValidator == nil {
		return nil, errNilCollaborator
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.NewID == nil {
		d.NewID = newReportID
	}
	if d.Budget <= 0 || d.Budget > DefaultCycleBudget-CommitReserve {
		d.Budget = DefaultCycleBudget - CommitReserve
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &DiscoveryPipeline{d: d, selector: &AdvanceSelector{Probe: d.Probe, Now: d.Clock, Validator: d.AdvanceValidator}}, nil
}

func newReportID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "refresh-" + hex.EncodeToString(b[:])
}

// Collect proposes only fully evidenced replacements. A failed source or
// installation inventory yields a journalable degraded cycle without proposals.
// Prospective collection never proposes mutations and propagates route faults;
// unreadable settings, catalog/policy state and caller cancellation are errors.
func (p *DiscoveryPipeline) Collect(ctx context.Context, prospective *provider.Profile) (CycleCommit, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultCycleBudget)
	defer cancel()
	commit, err := p.collect(ctx, prospective)
	if err != nil {
		return commit, err
	}
	// The webhook state a discovery event is frozen with is read fresh, after
	// the slow fetch and probes, and separately from the catalog-URL read: a
	// fault here leaves WebhookConfigured nil (awaiting_setting) rather than
	// discarding a completed discovery or posing as an empty webhook.
	commit.WebhookConfigured = nil
	if fresh, err := p.d.Settings.ReadInternal(ctx); err == nil {
		configured := fresh.TeamsWebhookURL != ""
		commit.WebhookConfigured = &configured
	} else {
		p.d.Logger.Warn("webhook eligibility read failed; discovery events await the setting", "error", err)
	}
	return commit, nil
}

func (p *DiscoveryPipeline) collect(parent context.Context, prospective *provider.Profile) (CycleCommit, error) {
	deadline := time.Now().Add(p.d.Budget)
	if end, ok := parent.Deadline(); ok && end.Add(-CommitReserve).Before(deadline) {
		deadline = end.Add(-CommitReserve)
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	values, err := p.d.Settings.ReadInternal(ctx)
	if err != nil {
		return CycleCommit{}, fmt.Errorf("reading refresh settings: %w", err)
	}
	stored, err := p.d.Store.Snapshot(ctx)
	if err != nil {
		return CycleCommit{}, fmt.Errorf("reading stored catalog: %w", err)
	}
	now := p.d.Clock()
	commit := CycleCommit{
		Report: RefreshReport{ID: p.d.NewID(), At: now, SliceStatus: map[string]string{}, PathStatus: map[string]string{}},
	}
	// Failure baseline: keep the stored catalog and diagnostics as they are.
	keep := func(status string) (CycleCommit, error) {
		if err := parent.Err(); err != nil {
			return CycleCommit{}, err
		}
		if ctx.Err() != nil {
			status = "budget_exhausted"
		}
		commit.Catalog = stored.Catalog
		commit.UnmatchedTargets = stored.UnmatchedTargets
		commit.Evidence, commit.Advances, commit.ProbeCursor = nil, nil, nil
		commit.PolicyRevision = 0
		commit.Report.PathStatus["catalog"] = status
		return commit, nil
	}

	raw, err := p.d.Fetcher.Fetch(ctx, values.CatalogURL)
	if err != nil {
		p.d.Logger.Warn("model catalog fetch failed", "error", err)
		return keep("fetch_failed")
	}
	snap, err := p.d.Normalizer.Normalize(ctx, values.CatalogURL, raw)
	if err != nil {
		p.d.Logger.Warn("model catalog parse failed", "error", err)
		return keep("parse_error")
	}
	commit.Catalog = snap
	commit.PolicyRevision = classifiedRevision(snap)
	commit.UnmatchedTargets = stored.UnmatchedTargets
	for _, sl := range snap.Slices {
		commit.Report.SliceStatus[sl.Provider] = sl.Status
	}

	profiles, err := p.d.Profiles.ListInternal(ctx)
	if err != nil {
		if prospective != nil {
			return CycleCommit{}, fmt.Errorf("reading bootstrap profiles: %w", err)
		}
		commit.Report.PathStatus["profiles"] = UsabilityUnknown
		return p.finish(parent, commit)
	}
	var paths PathSnapshot
	if prospective != nil {
		inventory, ok := p.d.Inventory.(CandidateInventory)
		if !ok {
			return CycleCommit{}, errNilCollaborator
		}
		paths, err = inventory.PathsForProfile(ctx, prospective.Clone())
	} else {
		paths, err = p.d.Inventory.Snapshot(ctx)
	}
	if err != nil {
		if prospective != nil {
			return CycleCommit{}, err
		}
		p.d.Logger.Warn("execution path inventory incomplete", "error", err)
		commit.Report.PathStatus["inventory"] = UsabilityUnknown
		return p.finish(parent, commit)
	}

	remapped, err := remapView(ctx, p.d.Mapper, CatalogView{Catalog: snap, HistoricalModels: stored.HistoricalModels, HistoricalEntries: stored.HistoricalEntries})
	if err != nil {
		return CycleCommit{}, fmt.Errorf("remapping catalog: %w", err)
	}
	snap = remapped.Catalog
	commit.Catalog = snap
	known := knownEntries(snap, remapped)
	for _, e := range known {
		if e.PolicyRevision != paths.ExecutionRevision.PolicyRevision {
			return CycleCommit{}, provider.ErrExecutionRevisionStale
		}
	}
	commit.PolicyRevision = paths.ExecutionRevision.PolicyRevision
	sel := p.selector.selectWithin(parent, ctx, SelectionInput{
		RefreshID: commit.Report.ID, Profiles: profiles, Catalog: snap, Prospective: prospective,
		Known: known, Stored: stored.Evidence, Paths: paths, ProbeCursor: stored.ProbeCursor,
	})
	if err := parent.Err(); err != nil {
		return CycleCommit{}, err
	}
	commit.Evidence, commit.Advances, commit.ProbeCursor = sel.Evidence, sel.Advances, sel.Cursor
	if prospective != nil {
		commit.Advances = nil
	}
	commit.UnmatchedTargets = sel.Unmatched
	for k, v := range sel.PathStatus {
		commit.Report.PathStatus[k] = v
	}
	if sel.Exhausted {
		commit.Report.PathStatus["probe"] = "budget_exhausted"
		// Selection only proposes fully evidenced replacements. Unfinished or
		// failed paths keep their model without blocking independent profiles.
	}
	if sel.DiscoveryExhausted {
		// Proposals were fully evidenced; only the no-follower sweep was cut short.
		commit.Report.PathStatus["discovery"] = "budget_exhausted"
	}
	return p.finish(parent, commit)
}

func (p *DiscoveryPipeline) finish(ctx context.Context, c CycleCommit) (CycleCommit, error) {
	if err := ctx.Err(); err != nil {
		return CycleCommit{}, err
	}
	return c, nil
}

// classifiedRevision is the policy revision the normalizer mapped snap under;
// CommitCycle refuses entries that disagree with it.
func classifiedRevision(snap CatalogSnapshot) int64 {
	for _, sl := range snap.Slices {
		if sl.Status == SliceStatusOK && len(sl.Entries) > 0 {
			return sl.Entries[0].PolicyRevision
		}
	}
	return 0
}

// knownEntries is every identity the selector may compare: the freshly mapped
// slices plus historical rows (models absent from the latest slice) remapped
// under the same persisted policy, so both sides of a comparison share a
// revision.
func knownEntries(snap CatalogSnapshot, stored CatalogView) []modelidentity.Entry {
	var out []modelidentity.Entry
	have := map[string]bool{}
	for _, sl := range snap.Slices {
		if sl.Status != SliceStatusOK {
			continue
		}
		for _, e := range sl.Entries {
			out = append(out, e)
			have[e.Provider+"\x00"+e.NativeID] = true
		}
	}
	for _, e := range stored.HistoricalEntries {
		if !have[e.Provider+"\x00"+e.NativeID] {
			out = append(out, e)
		}
	}
	return out
}

// Publisher confirms committed advancements in the live profile view.
type Publisher = provider.AdvancementPublisher

// Gate is the execution gate the commit runs under.
type Gate interface {
	WithCurrent(ctx context.Context, expected string, fn func(context.Context) error) error
	WithRead(ctx context.Context, fn func(context.Context) error) error
}

// Announcer drains durable delivery after every cycle, including failed ones.
type Announcer interface {
	Announce(context.Context, RefreshReport) error
}

// RefreshService runs one full cycle: collect, commit under the execution gate,
// publish to new runs, acknowledge. It is the trusted internal entry; admin
// authorization belongs to the tool that wraps it.
type RefreshService struct {
	Pipeline  *DiscoveryPipeline
	Store     CatalogStore
	Gate      Gate
	Publisher Publisher
	Journal   provider.PublicationJournal
	Announcer Announcer
	Logger    *slog.Logger

	mu      sync.Mutex
	started bool
}

// DeferredAnnouncer leaves events in the durable queue during staged assembly.
type DeferredAnnouncer struct{}

func (DeferredAnnouncer) Announce(context.Context, RefreshReport) error { return nil }

// SetAnnouncer is an assembly-time operation, never a live delivery switch.
func (s *RefreshService) SetAnnouncer(a Announcer) error {
	if s == nil || a == nil {
		return errNilCollaborator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("modelcatalog: refresh lifecycle already started")
	}
	s.Announcer = a
	return nil
}

func (s *RefreshService) begin() error {
	if s == nil {
		return errNilCollaborator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return err
	}
	s.started = true
	return nil
}

func (s *RefreshService) validate() error {
	if s == nil || s.Pipeline == nil || s.Store == nil || s.Gate == nil || s.Publisher == nil || s.Journal == nil || s.Announcer == nil {
		return errNilCollaborator
	}
	return nil
}

func (s *RefreshService) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Refresh returns the committed report. An error leaves the working models as
// they were; a report returned with ErrPublicationPending is durable and is
// published by the next Recover or cycle.
func (s *RefreshService) Refresh(ctx context.Context) (report RefreshReport, err error) {
	if err = s.begin(); err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultCycleBudget)
	defer cancel()
	defer func() { s.announce(ctx, &report) }()
	// Repair original journal rows before another selection/CAS, even if this
	// cycle's source is unavailable. The coordinator serializes both operations.
	if err = s.Recover(ctx); err != nil {
		return report, fmt.Errorf("%w: %w", provider.ErrPublicationPending, err)
	}
	commit, err := s.Pipeline.Collect(ctx, nil)
	if err != nil {
		return report, err
	}
	report, err = s.commit(ctx, commit)
	if err != nil {
		return report, err
	}
	if err = s.publish(ctx, report); err != nil {
		return report, fmt.Errorf("%w: %w", provider.ErrPublicationPending, err)
	}
	report.PublicationPending = false
	return report, nil
}

// announce drains pending announcements after every refresh result, a failed
// cycle included: delivery of earlier events is independent of discovery.
func (s *RefreshService) announce(ctx context.Context, report *RefreshReport) {
	if err := s.Announcer.Announce(ctx, *report); err != nil {
		report.AnnouncementError = "announcement_error"
		s.logger().Warn("model announcement drain failed", "code", report.AnnouncementError)
	}
}

// commit writes the cycle while holding the gate: proposals only against the
// exact path revision they were proved on. A configuration change since
// collection drops the proposals and still records the evidence and catalog;
// the working models stay for this cycle.
func (s *RefreshService) commit(ctx context.Context, c CycleCommit) (RefreshReport, error) {
	var report RefreshReport
	write := func(ctx context.Context) error {
		var err error
		report, err = s.Store.CommitCycle(ctx, c)
		return err
	}
	if len(c.Advances) == 0 {
		return report, s.Gate.WithRead(ctx, write)
	}
	err := s.Gate.WithCurrent(ctx, c.Advances[0].PathRevision, write)
	if errors.Is(err, provider.ErrExecutionRevisionStale) || errors.Is(err, provider.ErrExecutionRevisionUnavailable) {
		for _, req := range c.Advances {
			c.DeferDiscovery = append(c.DeferDiscovery, req.CanonicalID)
		}
		c.Advances = nil
		c.Report.PathStatus["refresh"] = "advance_conflict"
		return report, s.Gate.WithRead(ctx, write)
	}
	return report, err
}

// publish makes the committed changes visible to new runs and acknowledges the
// report, only after the live view is confirmed.
func (s *RefreshService) publish(ctx context.Context, report RefreshReport) error {
	if len(report.Changes) > 0 {
		if err := s.Publisher.PublishCommitted(ctx, report.Changes); err != nil {
			return err
		}
	}
	return s.Store.MarkPublished(ctx, report.ID)
}

// Recover replays publication of everything committed but not yet confirmed:
// reports first, then direct advancements that have no report. It runs once at
// startup and before each coordinated cycle, and is safe to repeat.
func (s *RefreshService) Recover(ctx context.Context) error {
	if err := s.begin(); err != nil {
		return err
	}
	reports, err := s.Store.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recovering catalog reports: %w", err)
	}
	var errs []error
	for _, r := range reports {
		if err := s.publish(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("report %s: %w", r.ID, err))
		}
	}
	pending, err := s.Journal.Unpublished(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("reading unpublished advancements: %w", err))...)
	}
	if len(pending) > 0 {
		if err := s.Publisher.PublishCommitted(ctx, pending); err != nil {
			errs = append(errs, err)
		} else {
			ids := make([]string, len(pending))
			for i, a := range pending {
				ids[i] = a.ID
			}
			if err := s.Journal.Acknowledge(ctx, ids); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
