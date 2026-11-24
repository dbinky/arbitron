package runner

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
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
	NonPortableReason string `json:"nonPortableReason"`
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
	// MoveRequired asks the session mover to recover continuity, never re-place.
	MoveRequired bool
	MoveReason   string
}

// PlacementPolicy tunes Phase 3 stay-versus-move; the zero value is Phase 1.
type PlacementPolicy struct {
	MoveLoadThresholdPct int
	MoveSizeCapBytes     int64
	TelemetryMaxAge      time.Duration
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

// placeRanked decides where req runs, or why it must wait. Pure: no I/O, no clock.
func placeRanked(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision {

	runners := map[RunnerID]Runner{}
	for _, r := range snap.Runners {
		if runnerEligible(r, req, snap) {
			runners[r.ID] = r
		}
	}

	var eligible []candidate
	var exhausted, loggedOut, probationBusy, matched int
	var nextReset *time.Time
	var blocked []string

	if req.Group.Kind == CredNone {
		for _, r := range runners {
			eligible = append(eligible, candidate{runner: r, used: snap.SlotsInUse[r.ID], penalty: telemetryPenalty(snap.Telemetry, r.ID), tie: tieBreak(req.RunID, "")})
		}
	} else {
		for i := range snap.Sources {
			s := snap.Sources[i]
			r, ok := runners[s.RunnerID]
			if !ok {
				continue
			}
			if !sourceMatches(s, req) {
				if s.UnavailableReason != "" && sourceRequestMatches(s, req) {
					blocked = append(blocked, s.UnavailableReason)
				}
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
		if len(blocked) > 0 {
			slices.Sort(blocked)
			blocked = slices.Compact(blocked)
			detail += fmt.Sprintf(" model=%s backend=%s blocked=%s", req.Model, req.Backend, strings.Join(blocked, ","))
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
	// Pins and built-in restrictions are cumulative; neither can relax the other.
	if req.PinnedRunner != "" && r.ID != req.PinnedRunner {
		return false
	}
	builtInOnly := req.Group.Kind == CredServerConnection || req.Group.Kind == CredLocalHarness || req.Affinity == AffinityBuiltIn
	if builtInOnly {
		return r.ID == BuiltInRunnerID
	}
	if req.PinnedRunner != "" {
		return true
	}
	switch req.Affinity {
	case AffinitySession:
		return r.ID == BuiltInRunnerID
	}
	return true
}

func sourceMatches(s Source, req ExecRequirements) bool {
	return s.UnavailableReason == "" && sourceRequestMatches(s, req)
}

func sourceRequestMatches(s Source, req ExecRequirements) bool {
	if req.RequiredSource != "" && s.ID != req.RequiredSource {
		return false
	}
	g := req.Group
	if s.Kind != g.Kind {
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
	return s.RunnerID == BuiltInRunnerID || slices.ContainsFunc(s.Capabilities, func(c ExecutionCapability) bool { return capabilitySatisfies(c, req) })
}

// capabilitySatisfies is the exact remote eligibility predicate. Built-in
// sources retain their inner factory validation instead.
func capabilitySatisfies(c ExecutionCapability, req ExecRequirements) bool {
	if c.Op != req.Op || c.Backend != req.Backend || c.EndpointFingerprint != req.EndpointFingerprint ||
		provider.Canonicalize(c.Provider) != provider.Canonicalize(req.Provider) ||
		!slices.Contains(c.Families, req.Group.Family) || !slices.Contains(c.Models, req.Model) {
		return false
	}
	if req.ContextWindow != "" && req.ContextWindow != provider.ContextDefault && !slices.Contains(c.ContextWindows, req.ContextWindow) {
		return false
	}
	if req.Effort != "" && req.Effort != provider.EffortDefault && !slices.Contains(c.Efforts, req.Effort) {
		return false
	}
	for _, tool := range req.Tools {
		if !slices.Contains(c.Tools, tool) {
			return false
		}
	}
	return !req.RequireExecutedModel || c.ExecutedModel
}

func headroomTier(h *float64) int {
	switch {
	case h == nil:
		return 1
	case *h > lowHeadroom:
		return 0
	default:
		return 2
	}
}

// better ranks headroom, utilisation, machine pressure, then a per-run tie-break.
func better(a, b candidate) bool {
	if ta, tb := headroomTier(a.headroom), headroomTier(b.headroom); ta != tb {
		return ta < tb
	}
	if a.headroom != nil && b.headroom != nil && *a.headroom != *b.headroom {
		return *a.headroom > *b.headroom
	}
	ua := float64(a.used) / float64(a.runner.StepLimit)
	ub := float64(b.used) / float64(b.runner.StepLimit)
	if ua != ub {
		return ua < ub
	}
	if a.penalty != b.penalty {
		return a.penalty < b.penalty
	}
	if a.tie != b.tie {
		return a.tie < b.tie
	}
	return candidateKey(a) < candidateKey(b)
}

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
