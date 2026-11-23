package runner

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/google/uuid"
)

// Telemetry is a runner's latest machine sample. Every field is optional:
// nil means unknown, never zero usage or proof of health.
type Telemetry struct {
	SampledAt     time.Time
	CPUPercent    *float64
	MemUsedBytes  *uint64
	MemPressure   *string // "normal"|"warn"|"critical"
	Load1Pct      *float64
	DiskFreeBytes *uint64
	Thermal       *string // "nominal"|"fair"|"serious"|"critical"
	UptimeSeconds *uint64
	Cores         *int
	RAMBytes      *uint64
}

// Home is where a run's workspace lives (Phase 3). RunnerID "" = server
// checkpoint only.
type Home struct {
	RunID             uuid.UUID
	RunnerID          RunnerID
	Generation        int64
	CheckpointVersion int64
	Dirty             bool
}

// PoolSnapshot is everything placement reads. Now comes from the caller's
// clock; Place never reads time itself.
type PoolSnapshot struct {
	Runners        []Runner
	Sources        []Source
	SlotsInUse     map[RunnerID]int
	SourceInFlight map[SourceID]int
	Telemetry      map[RunnerID]Telemetry
	Homes          map[uuid.UUID]Home
	Now            time.Time
}

// WaitKind distinguishes the two waits.
type WaitKind string

const (
	WaitSlot     WaitKind = "slot"     // queued · slot: an eligible source exists, no slot free
	WaitCapacity WaitKind = "capacity" // waiting · capacity: no eligible source
)

// WaitReason explains why a step is not placed.
type WaitReason struct {
	Kind      WaitKind
	Group     ModelGroup
	Model     string
	NextReset *time.Time
	Detail    string
}

// Decision is either a placement (Placed) or a wait (Wait != nil).
type Decision struct {
	Placed bool
	Runner RunnerID
	Source SourceID
	Reason string
	Tier   string // Phase 3: "must_move"|"should_move"|"may_move_load"|"stay"|"" (non-workspace)
	Wait   *WaitReason
}

// PlacementPolicy tunes Phase 3 stay-versus-move; the zero value is Phase 1.
type PlacementPolicy struct {
	MoveLoadThresholdPct int
	MoveSizeCapBytes     int64
}

// lowHeadroom is the boundary below which a known headroom ranks after unknown.
const lowHeadroom = 0.2

type candidate struct {
	runner   Runner
	source   *Source // nil for slot-only (CredNone) work
	used     int
	headroom *float64
	penalty  int
	tie      uint32
}

// Place decides where req runs, or why it must wait. Pure: no I/O, no clock.
func Place(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision {
	_ = policy // Phase 1: zero policy; P3 T4 adds tiers here.

	runners := map[RunnerID]Runner{}
	for _, r := range snap.Runners {
		if runnerEligible(r, req, snap) {
			runners[r.ID] = r
		}
	}

	var eligible []candidate
	var exhausted, loggedOut, probationBusy, matched int
	var nextReset *time.Time

	if req.Group.Kind == CredNone {
		for _, r := range runners {
			eligible = append(eligible, candidate{runner: r, used: snap.SlotsInUse[r.ID], penalty: telemetryPenalty(snap.Telemetry, r.ID), tie: tieBreak(req.RunID, "")})
		}
	} else {
		for i := range snap.Sources {
			s := snap.Sources[i]
			r, ok := runners[s.RunnerID]
			if !ok || !sourceMatches(s, req) {
				continue
			}
			matched++
			h := s.Health
			if h.Capacity == CapacityExhausted {
				exhausted++
				if h.ResetAt != nil && (nextReset == nil || h.ResetAt.Before(*nextReset)) {
					t := *h.ResetAt
					nextReset = &t
				}
			}
			switch {
			case h.Auth == AuthLoggedOut:
				loggedOut++
				continue
			case h.Capacity == CapacityAvailable:
			case h.Capacity == CapacityProbation && snap.SourceInFlight[s.ID] == 0:
			case h.Capacity == CapacityProbation:
				probationBusy++
				continue
			default:
				continue
			}
			eligible = append(eligible, candidate{runner: r, source: &s, used: snap.SlotsInUse[r.ID], headroom: h.Headroom,
				penalty: telemetryPenalty(snap.Telemetry, r.ID), tie: tieBreak(req.RunID, s.ID)})
		}
	}

	if len(eligible) == 0 {
		var detail string
		if matched == 0 {
			detail = fmt.Sprintf("waiting · capacity: no source serves %s", req.Group)
		} else {
			detail = fmt.Sprintf("waiting · capacity: no eligible source for %s (exhausted=%d logged_out=%d probation_busy=%d)",
				req.Group, exhausted, loggedOut, probationBusy)
		}
		if nextReset != nil {
			detail += " next reset " + nextReset.UTC().Format(time.RFC3339)
		}
		return wait(WaitReason{Kind: WaitCapacity, Group: req.Group, Model: req.Model, NextReset: nextReset, Detail: detail})
	}

	free := eligible[:0:0]
	for _, c := range eligible {
		if c.used < c.runner.StepLimit {
			free = append(free, c)
		}
	}
	if len(free) == 0 {
		return wait(WaitReason{Kind: WaitSlot, Group: req.Group, Model: req.Model,
			Detail: fmt.Sprintf("queued · slot: every eligible runner for %s is at its step limit", req.Group)})
	}

	sort.SliceStable(free, func(i, j int) bool { return better(free[i], free[j]) })
	best := free[0]
	d := Decision{Placed: true, Runner: best.runner.ID, Reason: reason(best)}
	if best.source != nil {
		d.Source = best.source.ID
	}
	return d
}

func wait(w WaitReason) Decision { return Decision{Reason: w.Detail, Wait: &w} }

func runnerEligible(r Runner, req ExecRequirements, snap PoolSnapshot) bool {
	if r.Cordoned || r.Draining || r.Liveness != LivenessOnline {
		return false
	}
	switch r.Compat.State {
	case CompatIncompatible:
		return false
	case CompatChatOnly:
		if req.Affinity != AffinityNone {
			return false
		}
	}
	if req.PinnedRunner != "" {
		return r.ID == req.PinnedRunner
	}
	builtInOnly := req.Group.Kind == CredServerConnection || req.Group.Kind == CredLocalHarness || req.Affinity == AffinityBuiltIn
	if builtInOnly {
		return r.ID == BuiltInRunnerID
	}
	switch req.Affinity {
	case AffinityWorkspace:
		if h, ok := snap.Homes[req.RunID]; ok && h.RunnerID != "" {
			return r.ID == h.RunnerID
		}
		return r.ID == BuiltInRunnerID
	case AffinitySession:
		return r.ID == BuiltInRunnerID
	}
	return true
}

func sourceMatches(s Source, req ExecRequirements) bool {
	g := req.Group
	if s.UnavailableReason != "" || s.Kind != g.Kind {
		return false
	}
	switch g.Kind {
	case CredLunaRoute, CredServerConnection, CredLocalHarness:
		if s.CredentialRef != g.ConnectionRef {
			return false
		}
	}
	switch g.Kind {
	case CredServerConnection, CredLocalHarness:
		// The binding's own connection: an unresolved family matches only it.
		if g.Family != modelidentity.FamilyUnresolved && len(s.Families) > 0 && !slices.Contains(s.Families, g.Family) {
			return false
		}
	default:
		if g.Family == modelidentity.FamilyUnresolved || g.Family == "" || !slices.Contains(s.Families, g.Family) {
			return false
		}
	}
	if len(s.Backends) > 0 && !slices.Contains(s.Backends, req.Backend) {
		return false
	}
	if len(s.Models) > 0 && !slices.Contains(s.Models, req.Model) {
		return false
	}
	return true
}

// better orders candidates. Cycle 2 replaces this with the full ranking
// (headroom tier, utilisation, telemetry penalty, fair tie-break).
func better(a, b candidate) bool { return candidateKey(a) < candidateKey(b) }

func candidateKey(c candidate) string {
	if c.source != nil {
		return string(c.source.ID)
	}
	return string(c.runner.ID)
}

// tieBreak spreads equal candidates deterministically per run.
func tieBreak(runID uuid.UUID, src SourceID) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID.String() + string(src)))
	return h.Sum32()
}

// telemetryPenalty scores known machine pressure; unknown telemetry is neutral (0).
func telemetryPenalty(tel map[RunnerID]Telemetry, id RunnerID) int {
	t, ok := tel[id]
	if !ok {
		return 0
	}
	p := 0
	if t.MemPressure != nil {
		switch *t.MemPressure {
		case "warn":
			p++
		case "critical":
			p += 2
		}
	}
	if t.Thermal != nil {
		switch *t.Thermal {
		case "serious":
			p++
		case "critical":
			p += 2
		}
	}
	if t.CPUPercent != nil && *t.CPUPercent >= 90 {
		p++
	}
	return p
}

func reason(c candidate) string {
	src := "-"
	if c.source != nil {
		src = string(c.source.ID)
	}
	head := "unknown"
	if c.headroom != nil {
		head = fmt.Sprintf("%.2f", *c.headroom)
	}
	return fmt.Sprintf("rank: runner=%s source=%s headroom=%s slots=%d/%d", c.runner.ID, src, head, c.used, c.runner.StepLimit)
}
