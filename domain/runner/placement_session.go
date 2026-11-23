package runner

import (
	"fmt"
	"sort"
)

// placeSession places an interactive turn. No pin (first turn): exactly a
// workspace placement. A pin: that runner only — eligible ⇒ placed ("stay");
// operator state, liveness doubt, slot pressure or congestion ⇒ wait on the
// pin; exhaustion, logout, drain or loss ⇒ must_move. Load never moves a pin.
func placeSession(req ExecRequirements, snap PoolSnapshot, policy PlacementPolicy) Decision {
	if req.PinnedRunner == "" {
		first := req
		first.Affinity = AffinityWorkspace
		return Place(first, snap, policy)
	}
	pin := req.PinnedRunner
	var r *Runner
	for i := range snap.Runners {
		if snap.Runners[i].ID == pin {
			r = &snap.Runners[i]
			break
		}
	}
	switch {
	case r == nil || r.Liveness == Liveness("lost"):
		return mustMove(pin, "runner_lost")
	case r.Draining:
		return mustMove(pin, "drain")
	case r.Liveness == Liveness("offline"):
		return waitOnPin(req, WaitKind("slot"), "offline")
	case r.Liveness != LivenessOnline:
		return waitOnPin(req, WaitSlot, string(r.Liveness))
	case r.Cordoned:
		return waitOnPin(req, WaitKind("slot"), "cordoned")
	case r.Compat.State != CompatCompatible:
		return waitOnPin(req, WaitKind("slot"), "incompatible: "+r.Compat.Reason)
	}

	var matching []Source
	for _, s := range snap.Sources {
		if s.RunnerID == pin && sourceRequestMatches(s, req) {
			matching = append(matching, s)
		}
	}
	if len(matching) == 0 {
		return waitOnPin(req, WaitKind("capacity"), "source inventory unavailable")
	}
	var usable []Source
	loggedOut, exhausted, busy, congested := 0, 0, 0, 0
	for _, s := range matching {
		h := s.Health
		switch {
		case s.UnavailableReason != "":
			busy++ // unavailable capability waits on pin; it does not prove logout
		case h.Capacity != CapacityState("available") && h.Capacity != CapacityState("probation") && h.Capacity != CapacityState("exhausted"):
			busy++
		case h.Auth == AuthState("logged_out"):
			loggedOut++
		case h.Auth != AuthOK:
			busy++
		case h.Capacity == CapacityState("exhausted"):
			exhausted++
		case h.Capacity == CapacityState("probation") && snap.SourceInFlight[s.ID] > 0:
			busy++
		case h.Congested && (h.CongestedUntil == nil || h.CongestedUntil.After(snap.Now)):
			congested++
		default:
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		switch {
		case loggedOut == len(matching):
			return mustMove(pin, "logged_out")
		case exhausted > 0 && busy == 0 && congested == 0:
			return mustMove(pin, "exhausted")
		case congested > 0:
			return waitOnPin(req, WaitKind("capacity"), "congested")
		default:
			return waitOnPin(req, WaitKind("capacity"), "probation")
		}
	}
	used := snap.SlotsInUse[pin]
	if used >= r.StepLimit {
		return waitOnPin(req, WaitKind("slot"), fmt.Sprintf("slots %d/%d", used, r.StepLimit))
	}
	sort.SliceStable(usable, func(i, j int) bool {
		rank := func(s *Source) candidate {
			return candidate{runner: *r, source: s, used: used, headroom: s.Health.Headroom, penalty: telemetryPenalty(snap.Telemetry, pin), tie: tieBreak(req.RunID, s.ID)}
		}
		return better(rank(&usable[i]), rank(&usable[j]))
	})
	best := usable[0]
	headroom := "unknown"
	if best.Health.Headroom != nil {
		headroom = fmt.Sprintf("%.2f", *best.Health.Headroom)
	}
	return Decision{
		Placed: true, Runner: pin, Source: best.ID, Tier: "stay",
		Reason: fmt.Sprintf("session_pin: runner=%s source=%s headroom=%s slots=%d/%d", pin, best.ID, headroom, used, r.StepLimit),
	}
}

func mustMove(pin RunnerID, reason string) Decision {
	return Decision{Tier: "must_move", MoveRequired: true, MoveReason: reason,
		Reason: fmt.Sprintf("must_move: pinned runner=%s reason=%s", pin, reason)}
}

func waitOnPin(req ExecRequirements, kind WaitKind, detail string) Decision {
	return Decision{Tier: "stay", Wait: &WaitReason{Kind: kind, Group: req.Group, Model: req.Model,
		Detail: fmt.Sprintf("session pinned to %s: %s", req.PinnedRunner, detail)}}
}
