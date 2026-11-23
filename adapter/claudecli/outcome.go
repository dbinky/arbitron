package claudecli

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	pool "github.com/arbitron/arbitron/domain/runner"
)

// rateLimitInfo is a decoded rate_limit_info / quotaLimits object: status,
// reset (epoch seconds; milliseconds tolerated), window type, utilization 0..1.
type rateLimitInfo struct {
	Status        string
	ResetsAt      *float64
	RateLimitType string
	Utilization   *float64
}

type rawRateLimitInfo struct {
	Status        string   `json:"status"`
	ResetsAt      *float64 `json:"resetsAt"`
	RateLimitType string   `json:"rateLimitType"`
	Utilization   *float64 `json:"utilization"`
}

// outcomeSignal is the structured, prose-free failure evidence on one event.
type outcomeSignal struct {
	ErrorKind      string
	APIErrorStatus int
	Limit          *rateLimitInfo
}

// decodeSignal leniently extracts the outcome fields from an envelope. A field
// of an unexpected JSON type is ignored (never fails the line), so unknown or
// changed shapes fail closed to "no signal" => class other.
func decodeSignal(env rawEnvelope) *outcomeSignal {
	var s outcomeSignal
	var kind string
	if len(env.Error) > 0 && json.Unmarshal(env.Error, &kind) == nil {
		s.ErrorKind = strings.TrimSpace(kind)
	}
	for _, raw := range []json.RawMessage{env.APIErrorStatus, env.APIErrorStatusCamel} {
		var n int
		if len(raw) > 0 && json.Unmarshal(raw, &n) == nil && n != 0 {
			s.APIErrorStatus = n
		}
	}
	candidates := []json.RawMessage{env.RateLimitInfo, env.QuotaLimits}
	if len(env.APIErrorParams) > 0 {
		var p struct {
			RateLimitInfo json.RawMessage `json:"rate_limit_info"`
		}
		if json.Unmarshal(env.APIErrorParams, &p) == nil {
			candidates = append(candidates, p.RateLimitInfo)
		}
	}
	for _, raw := range candidates {
		if len(raw) == 0 {
			continue
		}
		var r rawRateLimitInfo
		if json.Unmarshal(raw, &r) != nil || r.Status == "" {
			continue
		}
		s.Limit = &rateLimitInfo{Status: r.Status, ResetsAt: r.ResetsAt, RateLimitType: r.RateLimitType, Utilization: r.Utilization}
		if r.Status == "rejected" {
			break // the rejecting window is the authoritative one
		}
	}
	if s.ErrorKind == "" && s.APIErrorStatus == 0 && s.Limit == nil {
		return nil
	}
	return &s
}

var loggedOutKinds = map[string]bool{
	"authentication_failed": true, "oauth_org_not_allowed": true, "account_on_hold": true,
	"verification_required": true, "billing_error": true,
}

var transientKinds = map[string]bool{"rate_limit": true, "overloaded": true, "server_error": true}

// outcomeTracker folds the structured signals seen across one claude turn.
type outcomeTracker struct {
	errorKind string
	status    int
	limit     *rateLimitInfo
	rejected  *rateLimitInfo // latched: a rejected window is never overwritten by a later allowed one
}

func (t *outcomeTracker) observe(ev streamEvent) {
	s := ev.Signal
	if s == nil {
		return
	}
	if s.ErrorKind != "" {
		t.errorKind = s.ErrorKind
	}
	if s.APIErrorStatus != 0 {
		t.status = s.APIErrorStatus
	}
	if s.Limit != nil {
		t.limit = s.Limit
		if s.Limit.Status == "rejected" {
			t.rejected = s.Limit
		}
	}
}

// classify returns the outcome, and ok=false when no structured evidence was seen.
// Order: logged-out (needs a human) > exhausted (rejected quota) > transient > other.
func (t *outcomeTracker) classify() (pool.SourceOutcome, bool) {
	switch {
	case loggedOutKinds[t.errorKind] || t.status == 401:
		return pool.SourceOutcome{Class: pool.OutcomeLoggedOut}, true
	case t.rejected != nil:
		o := pool.SourceOutcome{Class: pool.OutcomeExhausted, ResetAt: normalizeEpoch(t.rejected.ResetsAt)}
		if w, ok := windowFor(*t.rejected, o.ResetAt); ok {
			o.Windows = []pool.LimitWindow{w}
		}
		return o, true
	case transientKinds[t.errorKind] || t.status == 429 || (t.status >= 500 && t.status <= 599):
		return pool.SourceOutcome{Class: pool.OutcomeTransient}, true
	case t.errorKind != "" || t.status != 0:
		return pool.SourceOutcome{Class: pool.OutcomeOther}, true
	}
	return pool.SourceOutcome{}, false
}

// attach wraps err with the classified outcome. It MUST run before
// SanitizeProcessError so the class survives sanitization (via As).
func (t *outcomeTracker) attach(err error) error {
	if err == nil {
		return nil
	}
	if o, ok := t.classify(); ok {
		return pool.WithOutcome(err, o)
	}
	return err
}

// reportSuccess publishes an allowed_warning headroom hint for a turn that
// completed. 1-utilization when reported; absent utilization stays unknown.
func (t *outcomeTracker) reportSuccess(ctx context.Context) {
	if t.limit == nil || t.limit.Status != "allowed_warning" {
		return
	}
	o := pool.SourceOutcome{Class: pool.OutcomeNone}
	if u := t.limit.Utilization; u != nil {
		h := clamp01(1 - *u)
		o.HeadroomHint = &h
	}
	if w, ok := windowFor(*t.limit, normalizeEpoch(t.limit.ResetsAt)); ok {
		o.Windows = []pool.LimitWindow{w}
	}
	pool.ObserveOutcome(ctx, o)
}

// windowFor keys the window by its rateLimitType (identity), model scope and
// duration — never by primary/secondary position.
func windowFor(l rateLimitInfo, reset *time.Time) (pool.LimitWindow, bool) {
	if l.RateLimitType == "" {
		return pool.LimitWindow{}, false
	}
	w := pool.LimitWindow{LimitID: l.RateLimitType, ResetAt: reset}
	switch {
	case l.RateLimitType == "five_hour":
		w.DurationMins = 300
	case strings.HasPrefix(l.RateLimitType, "seven_day"):
		w.DurationMins = 10080
		switch l.RateLimitType {
		case "seven_day_opus":
			w.ModelScope = "opus"
		case "seven_day_sonnet":
			w.ModelScope = "sonnet"
		}
	}
	if l.Utilization != nil {
		p := clamp01(*l.Utilization) * 100
		w.UsedPercent = &p
	}
	return w, true
}

// normalizeEpoch converts an epoch in seconds (or milliseconds, when > 1e12) to
// UTC. Non-positive or missing values are unknown (nil).
func normalizeEpoch(v *float64) *time.Time {
	if v == nil || *v <= 0 {
		return nil
	}
	sec := *v
	if sec > 1e12 {
		sec /= 1000
	}
	t := time.Unix(int64(sec), 0).UTC()
	return &t
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
