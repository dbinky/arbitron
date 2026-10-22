package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
)

var ErrCommitSourceMisconfigured = errors.New("workflow: commit source requires journal, repository, mutation coordinator, tree committer and relocator")

type CommitSourceDeps struct {
	Journal      RevisionJournal
	Repo         Repository
	Mutations    MutationCoordinator
	Publications RevisionPublicationStore
	Trees        TreeCommitter
	Registry     Registry
	Relocator    ArrangementRequester
	Logger       *slog.Logger
}
type CommitSource struct{ deps CommitSourceDeps }

var _ CommittedRevisionSource = (*CommitSource)(nil)
var _ DestinationSettler = (*CommitSource)(nil)

func NewCommitSource(d CommitSourceDeps) (*CommitSource, error) {
	for _, port := range []struct {
		name    string
		missing bool
	}{{"journal", d.Journal == nil}, {"repository", d.Repo == nil}, {"mutation coordinator", d.Mutations == nil}, {"tree committer", d.Trees == nil}, {"relocator", d.Relocator == nil}, {"publication/registry pair", (d.Registry == nil) != (d.Publications == nil)}} {
		if port.missing {
			return nil, fmt.Errorf("%w: missing %s", ErrCommitSourceMisconfigured, port.name)
		}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &CommitSource{deps: d}, nil
}

type ownRelocationKey struct{}

// withOwnRelocation marks ctx as running inside the relocation intent id, so
// the operation's own Stamp can settle its destination without being refused
// by the very intent that covers it. Other operations never carry the mark.
func withOwnRelocation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ownRelocationKey{}, id)
}

func ownsRelocation(ctx context.Context, id string) bool {
	own, ok := ctx.Value(ownRelocationKey{}).(string)
	return ok && own != "" && own == id
}

func sameDestination(s Scope, n string, t MutationTarget) bool { return s == t.Scope && n == t.Name }
func (c *CommitSource) quarantine(ctx context.Context, r ArrangementRequest, reason string) error {
	if err := c.deps.Journal.Quarantine(ctx, r.Revision, reason); err != nil {
		return err
	}
	c.deps.Logger.Warn("quarantined uncommitted workflow revision", "revision", r.Revision, "reason", reason)
	return nil
}
func (c *CommitSource) exactPublication(ctx context.Context, s Scope, n, rev string, id uuid.UUID) (bool, error) {
	if s.Kind != ScopeShared || c.deps.Publications == nil {
		return true, nil
	}
	p, ok, err := c.deps.Publications.PublicationOf(ctx, n)
	if err != nil {
		return false, err
	}
	return ok && p.Revision == rev && p.WorkflowID == id, nil
}

// Settle runs inside the caller's destination lock. Relocations require a
// separate global pass owning both endpoints; a one-lock read never expands it.
func (c *CommitSource) Settle(ctx context.Context, s Scope, n string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only intents keyed to this destination are read, so a damaged unrelated
	// intent cannot block it; a damaged matching one blocks it until repaired.
	intents, err := c.deps.Journal.RelocationIntentsAt(ctx, s, n)
	if err != nil {
		var damaged *DestinationError
		if errors.As(err, &damaged) {
			c.deps.Logger.Error("relocation intent needs operator repair; destination stays blocked", "scope", ScopeLabel(s), "owner", s.OwnerID, "name", n, "error", err)
		}
		return err
	}
	for _, in := range intents {
		if ownsRelocation(ctx, in.ID) {
			continue
		}
		if sameDestination(s, n, MutationTarget{in.From, in.FromName}) || sameDestination(s, n, MutationTarget{in.To, in.ToName}) {
			return ErrMutationPending
		}
	}
	entries, entryErr := c.deps.Journal.Entries(ctx)
	trees, treeErr := c.deps.Trees.PendingTreeCommits(ctx)
	if treeErr != nil {
		return errors.Join(entryErr, treeErr)
	}
	var errs []error
	if entryErr != nil {
		errs = append(errs, entryErr)
	}
	handled := map[string]bool{}
	for _, e := range entries {
		r := e.Request
		if r.Scope != s || r.Name != n {
			continue
		}
		var linked []TreeCommit
		for _, tree := range trees {
			if sameDestination(s, n, tree.Destination) && tree.Revision == r.Revision {
				linked = append(linked, tree)
				handled[tree.ID] = true
			}
		}
		if e.State == JournalCommitted {
			for _, tree := range linked {
				if err := c.deps.Trees.FinishTreeCommit(ctx, tree.ID); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		err := c.settlePrepared(ctx, r, linked)
		if err != nil {
			errs = append(errs, fmt.Errorf("settle %s: %w", r.Revision, err))
		}
	}
	// An incomplete journal scan cannot prove that an unlinked backup's input
	// was acknowledged and removed. Preserve it until enumeration succeeds.
	if entryErr != nil {
		return errors.Join(errs...)
	}
	// Enqueue acknowledgement may remove the journal just before backup cleanup.
	// Its remaining intent still has to be decided before the next authoring save.
	for _, tree := range trees {
		if handled[tree.ID] || !sameDestination(s, n, tree.Destination) {
			continue
		}
		wf, err := c.deps.Repo.Load(ctx, s, n)
		if err != nil && !errors.Is(err, ErrNotFound) {
			errs = append(errs, err)
			continue
		}
		proven := err == nil && wf.SavedRevision() == tree.Revision
		if proven {
			proven, err = c.exactPublication(ctx, s, n, tree.Revision, wf.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if proven {
			err = c.deps.Trees.FinishTreeCommit(ctx, tree.ID)
		} else {
			err = c.deps.Trees.RollbackTreeCommit(ctx, tree.ID)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (c *CommitSource) settlePrepared(ctx context.Context, r ArrangementRequest, trees []TreeCommit) error {
	wf, err := c.deps.Repo.Load(ctx, r.Scope, r.Name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	visible := err == nil && wf.SavedRevision() == r.Revision
	if visible {
		proven, err := c.exactPublication(ctx, r.Scope, r.Name, r.Revision, r.WorkflowID)
		if err != nil {
			return err
		}
		if proven && wf.ID == r.WorkflowID {
			if err = c.deps.Journal.MarkCommitted(ctx, r.Revision); err != nil {
				return err
			}
			for _, tree := range trees {
				if err = c.deps.Trees.FinishTreeCommit(ctx, tree.ID); err != nil {
					return err
				}
			}
			return nil
		}
		// A registry-backed replacement without proof must have a durable rollback.
		if len(trees) == 0 {
			return fmt.Errorf("%w: replacement has no rollback intent", ErrMutationPending)
		}
	}
	for _, tree := range trees {
		if err := c.deps.Trees.RollbackTreeCommit(ctx, tree.ID); err != nil {
			return err
		}
	}
	if err := c.releaseSeedClaim(ctx, r); err != nil {
		return err
	}
	reason := "not-committed"
	if visible {
		reason = "publish-not-recorded"
	}
	return c.quarantine(ctx, r, reason)
}

// releaseSeedClaim compensates only a fresh, abandoned seed under its
// destination lock. Keep the prepared input until compensation succeeds, and
// never release a claim protecting any surviving tree or publication.
func (c *CommitSource) releaseSeedClaim(ctx context.Context, r ArrangementRequest) error {
	if r.Reason != SaveReasonSeed || r.Scope != Shared() || r.WorkflowID == uuid.Nil || c.deps.Registry == nil {
		return nil
	}
	exists, err := c.deps.Repo.Exists(ctx, r.Scope, r.Name)
	if err != nil || exists {
		return err
	}
	_, present, err := c.deps.Publications.PublicationOf(ctx, r.Name)
	if err != nil || present {
		return err
	}
	entry, err := c.deps.Registry.Resolve(ctx, r.Name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if entry.ID != r.WorkflowID || entry.Name != r.Name || entry.Scope != ScopeShared || entry.OwnerID != uuid.Nil {
		return nil
	}
	return c.deps.Registry.Delete(ctx, r.WorkflowID)
}

func (c *CommitSource) resolveRelocation(ctx context.Context, in RelocationIntent) error {
	if c.deps.Registry == nil {
		return fmt.Errorf("%w: relocation requires registry authority", ErrMutationPending)
	}
	return c.deps.Mutations.WithMutations(ctx, []MutationTarget{{in.From, in.FromName}, {in.To, in.ToName}}, func(owned context.Context) error {
		name, err := c.deps.Registry.ResolveName(owned, in.WorkflowID)
		if err != nil {
			return err
		}
		entry, err := c.deps.Registry.Resolve(owned, name)
		if err != nil {
			return err
		}
		if entry.ID != in.WorkflowID {
			return ErrMutationPending
		}
		scope := Scope{Kind: entry.Scope, OwnerID: entry.OwnerID}
		if scope.Kind == ScopeShared {
			scope = Shared()
		}
		targetWins := scope == in.To && name == in.ToName
		sourceWins := scope == in.From && name == in.FromName
		if !targetWins && !sourceWins {
			return ErrMutationPending
		}
		// Registry location decides direction, but cannot prove the winning tree
		// is readable or still belongs to this workflow. Validate either winner
		// before moving history or deleting what may be the last surviving copy.
		winnerScope, winnerName := in.From, in.FromName
		if targetWins {
			winnerScope, winnerName = in.To, in.ToName
		}
		wf, err := c.deps.Repo.Load(owned, winnerScope, winnerName)
		if err != nil {
			return err
		}
		if wf.ID != in.WorkflowID {
			return ErrMutationPending
		}
		removeScope, removeName := in.To, in.ToName
		if targetWins {
			if err = c.deps.Journal.Relocate(owned, in.From, in.FromName, in.To, in.ToName); err != nil {
				return err
			}
			if err = c.deps.Relocator.RelocateArrangements(owned, in.From, in.FromName, in.To, in.ToName); err != nil {
				return err
			}
			removeScope, removeName = in.From, in.FromName
		}
		wf, err = c.deps.Repo.Load(owned, removeScope, removeName)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil {
			if wf.ID != in.WorkflowID {
				return ErrMutationPending
			}
			if err = c.deps.Repo.Delete(owned, removeScope, removeName); err != nil {
				return err
			}
		}
		// Only the registry-losing target's prepared rename is abandoned. Committed
		// history remains live and destination-only relocation never rewrites bytes.
		if sourceWins {
			entries, err := c.deps.Journal.Entries(owned)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if e.State == JournalPrepared && e.Request.Scope == in.To && e.Request.Name == in.ToName {
					if err = c.quarantine(owned, e.Request, "rename-not-committed"); err != nil {
						return err
					}
				}
			}
		}
		return c.deps.Journal.EndRelocation(owned, in.ID)
	})
}

// CommittedRequests enumerates only durable intents and journal entries, never
// the workflow catalog. Successful destinations survive errors at other ones.
// Failures confined to known destinations or records are *DestinationError;
// any other returned error is systemic.
func (c *CommitSource) CommittedRequests(ctx context.Context) ([]ArrangementRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var errs []error
	intents, err := c.deps.Journal.RelocationIntents(ctx)
	if err != nil {
		var damaged *DestinationError
		if errors.As(err, &damaged) {
			c.deps.Logger.Error("unreadable relocation intent left in place for operator repair; its endpoints stay blocked", "error", err)
		}
		errs = append(errs, err)
	}
	for _, in := range intents {
		if err := c.resolveRelocation(ctx, in); err != nil {
			errs = append(errs, &DestinationError{Targets: []MutationTarget{{in.From, in.FromName}, {in.To, in.ToName}}, Record: in.ID, Err: err})
		}
	}
	entries, err := c.deps.Journal.Entries(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	trees, err := c.deps.Trees.PendingTreeCommits(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	targets := map[MutationTarget]bool{}
	for _, e := range entries {
		targets[MutationTarget{e.Request.Scope, e.Request.Name}] = true
	}
	for _, tree := range trees {
		targets[tree.Destination] = true
	}
	ordered := make([]MutationTarget, 0, len(targets))
	for t := range targets {
		ordered = append(ordered, t)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		return ScopeLabel(a.Scope)+a.Scope.OwnerID.String()+a.Name < ScopeLabel(b.Scope)+b.Scope.OwnerID.String()+b.Name
	})
	for _, t := range ordered {
		// Systemic causes also surface unwrapped from the enumerations above, so
		// a destination's own settle failure is safely classified as local.
		if err := c.deps.Mutations.WithMutations(ctx, []MutationTarget{t}, func(owned context.Context) error { return c.Settle(owned, t.Scope, t.Name) }); err != nil {
			errs = append(errs, &DestinationError{Targets: []MutationTarget{t}, Err: err})
		}
	}
	entries, err = c.deps.Journal.Entries(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	var out []ArrangementRequest
	for _, e := range entries {
		if e.State == JournalCommitted {
			out = append(out, cloneArrangementRequest(e.Request))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RequestedAt.Equal(out[j].RequestedAt) {
			return out[i].Revision < out[j].Revision
		}
		return out[i].RequestedAt.Before(out[j].RequestedAt)
	})
	return out, errors.Join(errs...)
}
