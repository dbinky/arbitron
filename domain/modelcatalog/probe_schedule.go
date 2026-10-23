package modelcatalog

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

type probeWork struct {
	path     ExecutionPath
	entry    modelidentity.Entry
	follower bool
	prior    UsabilityEvidence
}

// collectProbes shares the bounded scan across follower and standalone paths.
// Unseen checks run before retries, oldest attempts first, with unknown
// before unavailable on tied timestamps. The persisted per-path/config cursor
// rotates equal-age checks with deterministic wraparound. Confirmations are
// never re-probed. A failed
// or timed-out attempt records unknown evidence so a hung head cannot consume
// every later cycle. No scheduling state constitutes model eligibility.
func (s *AdvanceSelector) collectProbes(ctx context.Context, in SelectionInput, known map[evidenceKey]UsabilityEvidence, out *Selection) {
	s.runProbeQueue(ctx, probeQueue(in, known, s.now()), known, out)
}

// Every probe is a paid or quota-consuming model turn on operator credentials,
// and the remote catalog decides which ids exist. These bounds keep that fan-out
// finite regardless of catalog size.
const (
	// maxProbesPerPathPerCycle caps the checks one path/configuration runs per
	// cycle. Follower checks take the leading slots; the persisted cursor
	// rotates the standalone remainder across cycles.
	maxProbesPerPathPerCycle = 8
	// probeRetryBase is the first retry delay after a non-confirming check; it
	// doubles with every consecutive failure.
	probeRetryBase = RefreshInterval / 2
	// maxProbeFailures retires a (model, path, configuration) from automatic
	// probing. A configuration change (new revision) starts over.
	maxProbeFailures = 6
)

// retryDue applies exponential backoff and retirement to non-confirming
// evidence. Legacy evidence with no failure count is due immediately.
func retryDue(prior UsabilityEvidence, now time.Time) bool {
	if prior.Failures <= 0 || prior.CheckedAt.IsZero() {
		return true
	}
	if prior.Failures >= maxProbeFailures {
		return false
	}
	return !now.Before(prior.CheckedAt.Add(probeRetryBase << (prior.Failures - 1)))
}

func probeQueue(in SelectionInput, known map[evidenceKey]UsabilityEvidence, now time.Time) []probeWork {
	prior := map[evidenceKey]UsabilityEvidence{}
	for _, ev := range in.Stored {
		prior[evidenceKey{ev.CanonicalID, ev.PathKey, ev.ConfigRevision}] = ev
	}
	groups := map[string][]probeWork{}
	var paths []string
	add := func(path ExecutionPath, e modelidentity.Entry, follower, operatorRequested bool) {
		key := evidenceKey{e.CanonicalID, path.Key, path.ConfigRevision}
		if evidenceMatches(known[key], path, e) {
			return
		}
		// An explicit create/seed request is operator-initiated and bounded to
		// one alias; automatic discovery honors backoff and retirement.
		if !operatorRequested && !retryDue(prior[key], now) {
			return
		}
		pk := path.Key + "@" + path.ConfigRevision
		if _, ok := groups[pk]; !ok {
			paths = append(paths, pk)
		}
		for i, work := range groups[pk] {
			if work.entry.CanonicalID == e.CanonicalID {
				groups[pk][i].follower = work.follower || follower
				return
			}
		}
		groups[pk] = append(groups[pk], probeWork{path: path, entry: e, follower: follower, prior: prior[key]})
	}
	if p := in.Prospective; p != nil && p.FollowTarget != nil {
		for _, e := range in.Known {
			if e.Provider != p.FollowTarget.Provider || (p.Model != "" && e.SpecificModel != p.Model && e.NativeID != p.Model) || (p.Model == "" && e.Tier != p.FollowTarget.Tier) {
				continue
			}
			for _, path := range consumingPaths(in.Paths, p.Alias) {
				add(path, e, true, true)
			}
		}
	}
	for _, p := range in.Profiles {
		if p.SelectionMode != provider.SelectionFollowing || p.FollowTarget == nil {
			continue
		}
		for _, e := range newerCandidates(p, in) {
			for _, path := range consumingPaths(in.Paths, p.Alias) {
				add(path, e, true, false)
			}
		}
	}
	for _, path := range in.Paths.Paths {
		catalogProvider := path.RouteProvider
		// D3/D4 subscription routes have no connection catalog provider.
		// Keep their actual execution binding/path identity unchanged; only
		// candidate enumeration uses the evidenced subscription family.
		if path.Backend == "codex" {
			catalogProvider = "openai"
		} else if catalogProvider == "claude-cli" {
			// pi with no connection runs only OAuth providers named by the
			// model's own prefix, which no anthropic catalog spelling carries.
			if path.Backend == "pi" {
				continue
			}
			catalogProvider = "anthropic"
		}
		if catalogProvider == "" || path.Key == "" || path.ConfigRevision == "" {
			continue
		}
		for _, e := range sourceCandidates(in, catalogProvider) {
			add(path, e, false, false)
		}
	}
	var queue []probeWork
	for _, pk := range paths {
		group := groups[pk]
		// Last considered ID is meaningful only on this exact path configuration.
		for i, work := range group {
			if work.entry.CanonicalID == in.ProbeCursor[pk] {
				group = append(slices.Clone(group[i+1:]), group[:i+1]...)
				break
			}
		}
		// Follower checks keep cursor order but lead the per-path budget.
		group = append(slices.DeleteFunc(slices.Clone(group), func(w probeWork) bool { return !w.follower }),
			slices.DeleteFunc(slices.Clone(group), func(w probeWork) bool { return w.follower })...)
		if len(group) > maxProbesPerPathPerCycle {
			group = group[:maxProbesPerPathPerCycle]
		}
		queue = append(queue, group...)
	}
	slices.SortStableFunc(queue, func(a, b probeWork) int {
		if d := a.prior.CheckedAt.Compare(b.prior.CheckedAt); d != 0 {
			return d
		}
		if a.prior.State == UsabilityUnavailable && b.prior.State != UsabilityUnavailable {
			return 1
		}
		if b.prior.State == UsabilityUnavailable && a.prior.State != UsabilityUnavailable {
			return -1
		}
		return 0
	})
	return queue
}

const probeWorkers = 4

// Workers own only isolated checks. Merge in dispatch order, not completion
// order, so concurrent I/O cannot scramble persisted cursors or diagnostics.
func (s *AdvanceSelector) runProbeQueue(ctx context.Context, queue []probeWork, known map[evidenceKey]UsabilityEvidence, out *Selection) {
	if len(queue) == 0 {
		return
	}
	type result struct {
		index     int
		selection Selection
	}
	jobs := make(chan int)
	results := make(chan result, probeWorkers)
	var workers sync.WaitGroup
	for range probeWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				work := queue[index]
				local := Selection{Cursor: map[string]string{}, PathStatus: map[string]string{}}
				s.check(ctx, work.path, work.entry, map[evidenceKey]UsabilityEvidence{}, &local)
				results <- result{index, local}
			}
		}()
	}
	go func() {
	dispatch:
		for index := range queue {
			if ctx.Err() != nil {
				break
			}
			select {
			case jobs <- index:
			case <-ctx.Done():
				break dispatch
			}
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	completed := make([]Selection, len(queue))
	for r := range results {
		completed[r.index] = r.selection
	}
	for i, local := range completed {
		work := queue[i]
		for key, cursor := range local.Cursor {
			out.Cursor[key] = cursor
		}
		for _, ev := range local.Evidence {
			if ev.Confirmed() {
				ev.Failures = 0
			} else {
				ev.Failures = work.prior.Failures + 1
			}
			known[evidenceKey{ev.CanonicalID, ev.PathKey, ev.ConfigRevision}] = ev
			out.Evidence = append(out.Evidence, ev)
			recordPathStatus(out, work.path.Key, ev.State)
		}
	}
	if ctx.Err() != nil {
		out.DiscoveryExhausted = true
		for _, work := range queue {
			if work.follower && !evidenceMatches(known[evidenceKey{work.entry.CanonicalID, work.path.Key, work.path.ConfigRevision}], work.path, work.entry) {
				out.Exhausted, out.DiscoveryExhausted = true, false
				break
			}
		}
	}
}
