package modelcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// SelectionInput is everything one selection reads, captured once per cycle.
// Known holds every catalog identity (current slices plus remapped historical
// rows) under the policy revision the paths were snapshotted at.
type SelectionInput struct {
	RefreshID string
	// Prospective prioritizes seed/create routes without proposing a mutation.
	Prospective *provider.Profile
	Profiles    []provider.Profile
	Catalog     CatalogSnapshot
	Known       []modelidentity.Entry
	Stored      []UsabilityEvidence
	// ProbeCursor is the last considered canonical ID for each path/configuration.
	// It controls scheduling only, never whether a model is usable.
	ProbeCursor map[string]string
	Paths       PathSnapshot
}

// Selection is the proposal set of one cycle. Evidence carries every fresh
// check, including unknown ones; Advances only models confirmed on every path.
type Selection struct {
	Advances   []provider.AdvanceRequest
	Evidence   []UsabilityEvidence
	Unmatched  []provider.FollowTarget
	Cursor     map[string]string
	PathStatus map[string]string
	// Exhausted reports that the probe budget ended during follower checks.
	// Advances still require complete confirmation on every consuming path.
	Exhausted bool
	// DiscoveryExhausted reports that the context ended during no-follower
	// discovery, after every proposal was already fully evidenced. The evidence
	// gathered so far is kept and the advances stand.
	DiscoveryExhausted bool
}

// AdvanceSelector decides which following profiles may move to a newer model.
// It enforces what the commit sink does not: a strictly newer model of the same
// provider and tier, ordered by modelidentity.Compare against a verified current
// identity; confirmed on every path that consumes the alias; one executable
// spelling. A pinned profile, an unknown current model and an unorderable pair
// never produce a proposal.
type AdvanceSelector struct {
	Probe     UsabilityProbe
	Now       func() time.Time
	Validator AdvanceValidator
}

type evidenceKey struct{ canonical, path, config string }

func (s *AdvanceSelector) Select(ctx context.Context, in SelectionInput) Selection {
	return s.selectWithin(ctx, ctx, in)
}

// selectWithin uses the live cycle context for read-only validation after a
// bounded scan, so a timed-out final probe cannot discard completed proofs.
func (s *AdvanceSelector) selectWithin(ctx, scan context.Context, in SelectionInput) Selection {
	out := Selection{Cursor: map[string]string{}, PathStatus: map[string]string{}}
	known := map[evidenceKey]UsabilityEvidence{}
	for _, ev := range in.Stored {
		if ev.Confirmed() {
			known[evidenceKey{ev.CanonicalID, ev.PathKey, ev.ConfigRevision}] = ev
		}
	}
	profiles := slices.Clone(in.Profiles)
	slices.SortFunc(profiles, func(a, b provider.Profile) int {
		switch {
		case a.Alias < b.Alias:
			return -1
		case a.Alias > b.Alias:
			return 1
		}
		return 0
	})
	in.Profiles = profiles
	s.collectProbes(scan, in, known, &out)
	if ctx.Err() != nil {
		return out
	}
	seenTarget := map[provider.FollowTarget]bool{}
	for _, p := range profiles {
		if p.SelectionMode != provider.SelectionFollowing || p.FollowTarget == nil {
			continue
		}
		target := *p.FollowTarget
		if !s.targetMatched(in.Catalog, target) {
			if s.sliceObserved(in.Catalog, target.Provider) && !seenTarget[target] {
				seenTarget[target] = true
				out.Unmatched = append(out.Unmatched, target)
			}
			continue
		}
		if _, reason := currentIdentity(p, in); reason != "" {
			out.PathStatus["profile:"+p.Alias] = reason
			continue
		}
		paths := consumingPaths(in.Paths, p.Alias)
		if len(paths) == 0 {
			out.PathStatus["profile:"+p.Alias] = "unavailable_paths"
			continue
		}
		cands := newerCandidates(p, in)
		for _, cand := range cands {
			ok := true
			refs := make([]provider.PathEvidenceRef, 0, len(paths))
			for _, path := range paths {
				refs = append(refs, provider.PathEvidenceRef{PathKey: path.Key, ConfigRevision: path.ConfigRevision})
				ev := known[evidenceKey{cand.CanonicalID, path.Key, path.ConfigRevision}]
				if !evidenceMatches(ev, path, cand) {
					ok = false
				}
			}
			if ok {
				req := provider.AdvanceRequest{
					ID: advanceID(p, cand), RefreshID: in.RefreshID, Expected: p.Clone(), Model: cand.SpecificModel,
					CanonicalID: cand.CanonicalID, PathRevision: in.Paths.Revision, ExecutionRevision: in.Paths.ExecutionRevision.Clone(),
					Evidence: refs, At: s.now(),
				}
				if s.Validator != nil {
					if err := s.Validator.ValidateAdvance(ctx, req); err != nil {
						out.PathStatus["profile:"+p.Alias] = "candidate_invalid"
						continue
					}
				}
				out.Advances = append(out.Advances, req)
				break
			}
		}
	}
	return out
}

// sourceCandidates enumerates source-backed identities independently of follow
// eligibility. Native-only, older and unclassified models need execution proof
// too: the spec announces every newly usable model, not only selectable ones.
// Stable ordering plus the persisted cursor, the per-path cap, retry backoff and
// the native-id allow-list bound the work (see probe_schedule.go).
func sourceCandidates(in SelectionInput, prov string) []modelidentity.Entry {
	var out []modelidentity.Entry
	seen := map[string]bool{}
	for _, sl := range in.Catalog.Slices {
		if sl.Provider != prov || sl.Status != SliceStatusOK {
			continue
		}
		for _, e := range sl.Entries {
			if e.Provider != prov || e.CanonicalID == "" || (e.NativeID == "" && e.SpecificModel == "") || seen[e.CanonicalID] {
				continue
			}
			seen[e.CanonicalID] = true
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b modelidentity.Entry) int {
		return strings.Compare(a.CanonicalID, b.CanonicalID)
	})
	return out
}

func (s *AdvanceSelector) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// check returns the confirmed evidence for (path, cand), probing only when no
// confirmation is stored for that exact path configuration. A failed or
// unverifiable check is recorded as unknown and never as usable.
func (s *AdvanceSelector) check(ctx context.Context, path ExecutionPath, cand modelidentity.Entry, known map[evidenceKey]UsabilityEvidence, out *Selection) (UsabilityEvidence, bool) {
	key := evidenceKey{cand.CanonicalID, path.Key, path.ConfigRevision}
	if ev := known[key]; evidenceMatches(ev, path, cand) {
		return ev, true
	}
	if err := ctx.Err(); err != nil {
		out.Exhausted = true
		return UsabilityEvidence{}, false
	}
	// Record every attempted turn, including timeouts and adapter errors. A
	// cursor is scheduling state only; failures can never manufacture proof.
	out.Cursor[path.Key+"@"+path.ConfigRevision] = cand.CanonicalID
	spelling := cand.SpecificModel
	if spelling == "" {
		spelling = cand.NativeID
	}
	ev, err := s.Probe.Check(ctx, path, cand)
	if err != nil || ev.CanonicalID != cand.CanonicalID || ev.PathKey != path.Key || ev.ConfigRevision != path.ConfigRevision {
		ev = UsabilityEvidence{CanonicalID: cand.CanonicalID, PathKey: path.Key,
			ConfigRevision: path.ConfigRevision, NativeModel: spelling, State: UsabilityUnknown}
	}
	if ctx.Err() != nil {
		out.Exhausted = true
	}
	if ev.CheckedAt.IsZero() {
		ev.CheckedAt = s.now()
	}
	// The selector, not the lane, knows which catalog offering it checked.
	ev.Provider = cand.Provider
	if ev.State == UsabilityConfirmed && !evidenceMatches(ev, path, cand) {
		ev.State = UsabilityUnknown
	}
	if ev.NativeModel == "" {
		ev.NativeModel = spelling
	}
	if ev.State != UsabilityConfirmed && ev.State != UsabilityUnavailable {
		ev.State = UsabilityUnknown
	}
	known[key] = ev
	out.Evidence = append(out.Evidence, ev)
	recordPathStatus(out, path.Key, ev.State)
	return ev, ev.Confirmed()
}

func recordPathStatus(out *Selection, path, status string) {
	switch out.PathStatus["path:"+path] {
	case UsabilityUnknown:
		status = UsabilityUnknown
	case UsabilityUnavailable:
		if status != UsabilityUnknown {
			status = UsabilityUnavailable
		}
	}
	out.PathStatus["path:"+path] = status
}

// evidenceMatches applies exact native identity and the pinned-only route rule
// equally to fresh observations, restart reuse and prospective revalidation.
// Empty provider provenance denotes pre-032 evidence; retain that legacy proof
// without attributing it to a catalog offering it never recorded.
func evidenceMatches(ev UsabilityEvidence, path ExecutionPath, cand modelidentity.Entry) bool {
	spelling := cand.SpecificModel
	if spelling == "" {
		spelling = cand.NativeID
	}
	family := provider.Canonicalize(path.RouteProvider)
	bindingFamily := provider.Canonicalize(path.Binding.Provider)
	opaque := path.Backend != "codex" && (family == provider.CanonicalAzure || family == provider.CanonicalBedrock || bindingFamily == provider.CanonicalAzure || bindingFamily == provider.CanonicalBedrock)
	return !opaque && ev.Confirmed() && ev.CanonicalID == cand.CanonicalID && ev.PathKey == path.Key && ev.ConfigRevision == path.ConfigRevision && ev.NativeModel == spelling && (ev.Provider == "" || ev.Provider == cand.Provider)
}

func (s *AdvanceSelector) sliceObserved(c CatalogSnapshot, prov string) bool {
	for _, sl := range c.Slices {
		if sl.Provider == prov && sl.Status == SliceStatusOK {
			return true
		}
	}
	return false
}

func (s *AdvanceSelector) targetMatched(c CatalogSnapshot, t provider.FollowTarget) bool {
	for _, sl := range c.Slices {
		if sl.Provider != t.Provider || sl.Status != SliceStatusOK {
			continue
		}
		for _, e := range sl.Entries {
			if e.Tier != "" && e.Tier == t.Tier {
				return true
			}
		}
	}
	return false
}

// currentIdentity refuses unresolved, mutable and ambiguous starting models:
// disappearance from the current slice alone never authorizes replacement.
func currentIdentity(p provider.Profile, in SelectionInput) (*modelidentity.Entry, string) {
	target := *p.FollowTarget
	policy := in.Paths.ExecutionRevision.PolicyRevision
	var current *modelidentity.Entry
	for i := range in.Known {
		e := in.Known[i]
		if e.Provider == target.Provider && (e.SpecificModel == p.Model || e.NativeID == p.Model) && e.PolicyRevision == policy {
			current = &e
			break
		}
	}
	if current != nil && current.Ambiguous {
		return nil, "ambiguous_identity"
	}
	if current == nil || current.Tier != target.Tier || current.OrderKey == "" || !current.Immutable || current.SpecificModel != p.Model {
		return nil, "unknown_current"
	}
	return current, ""
}

// newerCandidates returns every strictly newer eligible release, newest first.
func newerCandidates(p provider.Profile, in SelectionInput) []modelidentity.Entry {
	target := *p.FollowTarget
	policy := in.Paths.ExecutionRevision.PolicyRevision
	current, reason := currentIdentity(p, in)
	if reason != "" {
		return nil
	}
	var out []modelidentity.Entry
	for _, sl := range in.Catalog.Slices {
		if sl.Provider != target.Provider || sl.Status != SliceStatusOK {
			continue
		}
		for _, e := range sl.Entries {
			if e.Tier != target.Tier || e.OrderKey == "" || !e.Immutable || e.Ambiguous || e.SpecificModel == "" || e.PolicyRevision != policy {
				continue
			}
			if c, err := modelidentity.Compare(e, *current); err != nil || c <= 0 {
				continue
			}
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b modelidentity.Entry) int {
		c, err := modelidentity.Compare(b, a)
		if err != nil {
			return 0
		}
		return c
	})
	return out
}

func consumingPaths(snap PathSnapshot, alias string) []ExecutionPath {
	var out []ExecutionPath
	for _, p := range snap.Paths {
		if slices.Contains(p.Aliases, alias) {
			out = append(out, p)
		}
	}
	return out
}

// advanceID is stable across retries of the same advancement of the same
// profile revision to the same canonical model.
func advanceID(p provider.Profile, cand modelidentity.Entry) string {
	sum := sha256.Sum256([]byte(p.Alias + "\x00" + strconv.FormatInt(p.Revision, 10) + "\x00" + cand.CanonicalID + "\x00" + cand.SpecificModel))
	return "adv-" + hex.EncodeToString(sum[:12])
}

var errNilCollaborator = errors.New("modelcatalog: missing refresh collaborator")
