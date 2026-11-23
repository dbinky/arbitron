package runner

import (
	"fmt"
	"strings"
)

// Workspace stay-versus-move tiers (design phase 3 §3.2, D10).
const (
	TierMustMove    = "must_move"
	TierShouldMove  = "should_move"
	TierMayMoveLoad = "may_move_load"
	TierStay        = "stay"
)

// MinHomeFreeDiskBytes is the floor below which a home's KNOWN free disk is
// "confirmed insufficient" (tier 1). Unknown free disk never triggers a move.
const MinHomeFreeDiskBytes uint64 = 1 << 30

// Place chooses a runner and source for one dispatched operation. Workspace
// operations apply the stay-versus-move tiers around the run's home; every
// other affinity uses the Phase-1 ranking unchanged.
func Place(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision {
	snap = freshTelemetry(snap, policy)
	if req.Affinity == AffinityWorkspace {
		return placeWorkspace(req, snap, policy)
	}
	return placeRanked(req, snap, policy)
}

func placeWorkspace(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision {
	home, ok := snap.Homes[req.RunID]
	if ok && home.NonPortableReason != "" {
		req.Affinity = AffinityBuiltIn
		d := placeRanked(req, snap, policy)
		d.Tier = TierStay
		d.Reason = "non_portable: " + home.NonPortableReason + "; " + d.Reason
		return d
	}
	if !ok || home.RunnerID == ServerHome {
		return placeRanked(req, snap, policy)
	}
	others := withoutRunner(snap, home.RunnerID)
	hr, found := findRunner(snap.Runners, home.RunnerID)
	if !found {
		return moveTo(TierMustMove, req, others, policy, fmt.Sprintf("home=%s cause=%q", home.RunnerID, "home runner unavailable for this attempt"))
	}
	if hr.Liveness == Liveness("suspect") {
		return Decision{
			Tier:   TierStay,
			Reason: fmt.Sprintf("stay: home=%s suspect; waiting for reconnection or loss before any move", hr.ID),
			Wait: &WaitReason{Kind: WaitKind("slot"), Group: req.Group, Model: req.Model,
				Detail: fmt.Sprintf("workspace home %s is suspect; waiting for reconnection or loss", hr.ID)},
		}
	}
	if cause := homeIneligible(hr, snap.Telemetry[hr.ID]); cause != "" {
		return moveTo(TierMustMove, req, others, policy, fmt.Sprintf("home=%s cause=%q", hr.ID, cause))
	}
	atHome := placeRanked(req, onlyRunner(snap, hr.ID), policy)
	if !atHome.Placed {
		if atHome.Wait != nil && atHome.Wait.Kind == WaitKind("capacity") {
			inputs := fmt.Sprintf("home=%s cause=%q", hr.ID, "no eligible source at home (exhausted or logged out)")
			alt := placeRanked(req, others, policy)
			if !alt.Placed {
				// Wait over the whole pool so NextReset includes the home's own
				// exhausted sources (the home cannot be placed: it has no source).
				alt = placeRanked(req, snap, policy)
			}
			return retier(alt, TierMustMove, inputs)
		}
		used := snap.SlotsInUse[hr.ID]
		if alt := placeRanked(req, others, policy); alt.Placed {
			return retier(alt, TierShouldMove, fmt.Sprintf("home=%s home_slots=%d/%d", hr.ID, used, hr.StepLimit))
		}
		return retier(atHome, TierStay, fmt.Sprintf("home=%s home_slots=%d/%d", hr.ID, used, hr.StepLimit))
	}
	if req.Op != OpType("process") && loadMovesAllowed(req, policy) {
		if alt := placeRanked(req, others, policy); alt.Placed {
			homeHealth := degraded(snap.Telemetry[hr.ID])
			gap := utilisation(snap, hr) - utilisationOf(snap, alt.Runner)
			if degraded(snap.Telemetry[alt.Runner]) == "" && (homeHealth != "" || gap >= policy.MoveLoadThresholdPct) {
				hh := homeHealth
				if hh == "" {
					hh = "unknown_or_ok"
				}
				return retier(alt, TierMayMoveLoad, fmt.Sprintf("home=%s gap_pp=%d home_health=%q bytes=%d cap=%d",
					hr.ID, gap, hh, req.WorkspaceBytes, policy.MoveSizeCapBytes))
			}
		}
	}
	return retier(atHome, TierStay, fmt.Sprintf("home=%s", hr.ID))
}

// homeIneligible returns the tier-1 cause, or "" when the home may take the step.
func homeIneligible(r Runner, t Telemetry) string {
	switch {
	case r.Liveness == Liveness("lost") || r.Liveness == Liveness("offline"):
		return "home runner " + string(r.Liveness)
	case r.Draining:
		return "home runner draining"
	case r.Cordoned:
		return "home runner cordoned"
	case r.Compat.State == CompatState("incompatible"):
		return "home runner incompatible"
	case r.Compat.State == CompatState("chat_only"):
		return "home runner accepts chat only"
	case t.DiskFreeBytes != nil && *t.DiskFreeBytes < MinHomeFreeDiskBytes:
		return fmt.Sprintf("home free disk %d bytes below %d", *t.DiskFreeBytes, MinHomeFreeDiskBytes)
	}
	return ""
}

// degraded reports a KNOWN degraded health state; nil telemetry is never degraded.
func degraded(t Telemetry) string {
	if t.MemPressure != nil && *t.MemPressure == "critical" {
		return "memory pressure critical"
	}
	if t.Thermal != nil && (*t.Thermal == "serious" || *t.Thermal == "critical") {
		return "thermal " + *t.Thermal
	}
	return ""
}

func loadMovesAllowed(req ExecRequirements, p PlacementPolicy) bool {
	return p.MoveLoadThresholdPct > 0 && p.MoveSizeCapBytes > 0 &&
		req.WorkspaceBytes >= 0 && req.WorkspaceBytes <= p.MoveSizeCapBytes
}

func utilisation(snap PoolSnapshot, r Runner) int {
	if r.StepLimit <= 0 {
		return 100
	}
	return snap.SlotsInUse[r.ID] * 100 / r.StepLimit
}

func utilisationOf(snap PoolSnapshot, id RunnerID) int {
	if r, ok := findRunner(snap.Runners, id); ok {
		return utilisation(snap, r)
	}
	return 100
}

func moveTo(tier string, req ExecRequirements, others PoolSnapshot, policy PlacementPolicy, inputs string) Decision {
	return retier(placeRanked(req, others, policy), tier, inputs)
}

// retier replaces the ranking prefix of a Phase-1 reason with the tier and
// appends the tier inputs.
func retier(d Decision, tier, inputs string) Decision {
	rest := d.Reason
	if i := strings.Index(rest, ": "); i >= 0 {
		rest = rest[i+2:]
	}
	d.Tier = tier
	d.Reason = strings.TrimSpace(fmt.Sprintf("%s: %s %s", tier, rest, inputs))
	return d
}

func findRunner(rs []Runner, id RunnerID) (Runner, bool) {
	for _, r := range rs {
		if r.ID == id {
			return r, true
		}
	}
	return Runner{}, false
}

func withoutRunner(snap PoolSnapshot, id RunnerID) PoolSnapshot {
	out := snap
	out.Runners = nil
	for _, r := range snap.Runners {
		if r.ID != id {
			out.Runners = append(out.Runners, r)
		}
	}
	out.Sources = nil
	for _, s := range snap.Sources {
		if s.RunnerID != id {
			out.Sources = append(out.Sources, s)
		}
	}
	return out
}

func onlyRunner(snap PoolSnapshot, id RunnerID) PoolSnapshot {
	out := snap
	out.Runners = nil
	for _, r := range snap.Runners {
		if r.ID == id {
			out.Runners = append(out.Runners, r)
		}
	}
	out.Sources = nil
	for _, s := range snap.Sources {
		if s.RunnerID == id {
			out.Sources = append(out.Sources, s)
		}
	}
	return out
}

// freshTelemetry copies the metric map, keeping only samples with an authoritative
// timestamp inside the configured age window. It never changes the input snapshot.
func freshTelemetry(snap PoolSnapshot, policy PlacementPolicy) PoolSnapshot {
	out := make(map[RunnerID]Telemetry, len(snap.Telemetry))
	for id, sample := range snap.Telemetry {
		age := snap.Now.Sub(sample.SampledAt)
		if !sample.SampledAt.IsZero() && age >= 0 && age <= policy.TelemetryMaxAge {
			out[id] = sample
		}
	}
	snap.Telemetry = out
	return snap
}
