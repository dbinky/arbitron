package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	pool "github.com/arbitron/arbitron/domain/runner"
)

// errMalformedRolloutOutcome marks a rollout the outcome reader refuses. The
// caller then attaches nothing, so the failure classifies as "other".
var errMalformedRolloutOutcome = errors.New("codex: malformed rollout outcome record")

var transientErrorInfo = map[string]bool{
	"server_overloaded": true, "rate_limit_exceeded": true, "response_stream_disconnected": true,
	"internal_server_error": true, "http_connection_failed": true,
	"response_stream_connection_failed": true, "response_too_many_failed_attempts": true,
}

// classifiesRollout reports whether a binding's failures may be classified as
// subscription outcomes. LunaRoute launches never are (their health is the
// gateway's, and their private home is not the account's rollout store).
func classifiesRollout(b provider.ResolvedBinding) bool {
	return b.ConnectionRef == "" && !provider.IsLunaRoute(b.Provider)
}

type windowKey struct {
	limitID string
	scope   string
	mins    int
}

type rawRolloutWindow struct {
	UsedPercent     *float64 `json:"used_percent"`
	WindowMinutes   int      `json:"window_minutes"`
	ResetsAt        *float64 `json:"resets_at"`
	ResetsInSeconds *float64 `json:"resets_in_seconds"`
}

type rawRolloutRateLimits struct {
	LimitID     *string           `json:"limit_id"`
	LimitName   *string           `json:"limit_name"`
	Primary     *rawRolloutWindow `json:"primary"`
	Secondary   *rawRolloutWindow `json:"secondary"`
	ReachedType *string           `json:"rate_limit_reached_type"`
}

// readRolloutOutcome folds a rollout's event_msg records into an outcome:
// the LAST codex_error_info (from an `error` event or `task_complete.error`)
// decides the class; an object form with http_status_code 401/403 is logged
// out (observed on 0.171.0, codex-logged-out.md); token_count rate_limits
// supply windows (latest snapshot per limit/scope/duration key) and the reset.
// ok=false when the rollout carries no outcome evidence. Any unreadable record
// refuses the whole file (errMalformedRolloutOutcome), never a partial read.
func readRolloutOutcome(r io.Reader, now time.Time) (pool.SourceOutcome, bool, error) {
	lr := &io.LimitedReader{R: r, N: int64(maxRolloutBytes) + 1}
	br := bufio.NewReader(lr)
	var info string
	var status int // http_status_code of the last object-form codex_error_info, 0 = none
	var reached bool
	windows := map[windowKey]pool.LimitWindow{}
	for {
		line, err := readRolloutRecord(br, maxRolloutRecordBytes)
		if err == errRolloutRecordTooLarge || lr.N <= 0 {
			return pool.SourceOutcome{}, false, errMalformedRolloutOutcome
		}
		if err != nil && err != io.EOF {
			return pool.SourceOutcome{}, false, errMalformedRolloutOutcome
		}
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			var rec struct {
				Timestamp string          `json:"timestamp"`
				Type      string          `json:"type"`
				Payload   json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(line, &rec) != nil || rec.Type == "" {
				return pool.SourceOutcome{}, false, errMalformedRolloutOutcome
			}
			if rec.Type == "event_msg" {
				var p struct {
					Type       string          `json:"type"`
					ErrorInfo  json.RawMessage `json:"codex_error_info"`
					RateLimits json.RawMessage `json:"rate_limits"`
					Error      *struct {
						ErrorInfo json.RawMessage `json:"codex_error_info"`
					} `json:"error"` // task_complete only
				}
				if json.Unmarshal(rec.Payload, &p) != nil {
					return pool.SourceOutcome{}, false, errMalformedRolloutOutcome
				}
				switch p.Type {
				case "error":
					if v, st := decodeErrorInfo(p.ErrorInfo); v != "" {
						info, status = v, st
					}
				case "task_complete":
					if p.Error != nil {
						if v, st := decodeErrorInfo(p.Error.ErrorInfo); v != "" {
							info, status = v, st
						}
					}
				case "token_count":
					at := now
					if ts, terr := time.Parse(time.RFC3339Nano, rec.Timestamp); terr == nil {
						at = ts
					}
					if snap, ok := decodeRolloutRateLimits(p.RateLimits, at); ok {
						for k, w := range snap.windows {
							windows[k] = w // latest snapshot of the same key wins
						}
						reached = snap.reached
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
	}

	var o pool.SourceOutcome
	switch {
	case info == "unauthorized" || status == 401 || status == 403:
		o.Class = pool.OutcomeLoggedOut
	case info == "usage_limit_exceeded" || info == "usage_not_included" || (info == "" && reached):
		o.Class = pool.OutcomeExhausted
		o.ResetAt = exhaustedReset(windows)
	case transientErrorInfo[info]:
		o.Class = pool.OutcomeTransient
	case info != "":
		o.Class = pool.OutcomeOther
	default:
		return pool.SourceOutcome{}, false, nil
	}
	o.Windows = sortedWindows(windows)
	return o, true, nil
}

// decodeErrorInfo accepts the unit-variant string form ("usage_limit_exceeded")
// and the data-variant object form ({"http_connection_failed":{"http_status_code":401}}).
// status is the inner http_status_code when present and numeric, else 0.
func decodeErrorInfo(raw json.RawMessage) (info string, status int) {
	if len(raw) == 0 {
		return "", 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), 0
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && len(obj) == 1 {
		for k, inner := range obj {
			var d struct {
				HTTPStatusCode *int `json:"http_status_code"`
			}
			if json.Unmarshal(inner, &d) == nil && d.HTTPStatusCode != nil {
				status = *d.HTTPStatusCode
			}
			return strings.TrimSpace(k), status
		}
	}
	return "", 0
}

type rateSnapshot struct {
	windows map[windowKey]pool.LimitWindow
	reached bool
}

func decodeRolloutRateLimits(raw json.RawMessage, at time.Time) (rateSnapshot, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return rateSnapshot{}, false
	}
	var rl rawRolloutRateLimits
	if json.Unmarshal(raw, &rl) != nil {
		return rateSnapshot{}, false // lenient: an unreadable snapshot is ignored, not fatal
	}
	snap := rateSnapshot{windows: map[windowKey]pool.LimitWindow{}, reached: rl.ReachedType != nil && *rl.ReachedType != ""}
	id, scope := "codex", ""
	if rl.LimitID != nil && *rl.LimitID != "" {
		id = *rl.LimitID
	}
	if rl.LimitName != nil {
		scope = *rl.LimitName
	}
	for _, w := range []*rawRolloutWindow{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		lw := pool.LimitWindow{LimitID: id, ModelScope: scope, DurationMins: w.WindowMinutes, UsedPercent: w.UsedPercent}
		if t := normalizeResetEpoch(w.ResetsAt); t != nil {
			lw.ResetAt = t
		} else if w.ResetsInSeconds != nil && *w.ResetsInSeconds > 0 {
			t := at.Add(time.Duration(*w.ResetsInSeconds * float64(time.Second))).UTC()
			lw.ResetAt = &t
		}
		putWindow(snap.windows, lw)
	}
	return snap, true
}

// putWindow stores w under its identity key; within one snapshot two windows
// with the same key keep the more-used one (never silently lose exhaustion).
func putWindow(m map[windowKey]pool.LimitWindow, w pool.LimitWindow) {
	k := windowKey{limitID: w.LimitID, scope: w.ModelScope, mins: w.DurationMins}
	if prev, ok := m[k]; ok && used(prev) >= used(w) {
		return
	}
	m[k] = w
}

func used(w pool.LimitWindow) float64 {
	if w.UsedPercent == nil {
		return -1
	}
	return *w.UsedPercent
}

// exhaustedReset is the LATEST reset among windows at or above 100% (the source
// is unusable until every exhausted window resets); nil = unknown.
func exhaustedReset(ws map[windowKey]pool.LimitWindow) *time.Time {
	var out *time.Time
	for _, w := range ws {
		if used(w) >= 100 && w.ResetAt != nil && (out == nil || w.ResetAt.After(*out)) {
			t := *w.ResetAt
			out = &t
		}
	}
	return out
}

func sortedWindows(ws map[windowKey]pool.LimitWindow) []pool.LimitWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]pool.LimitWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LimitID != out[j].LimitID {
			return out[i].LimitID < out[j].LimitID
		}
		if out[i].ModelScope != out[j].ModelScope {
			return out[i].ModelScope < out[j].ModelScope
		}
		return out[i].DurationMins < out[j].DurationMins
	})
	return out
}

// normalizeResetEpoch converts epoch seconds (or ms when > 1e12) to UTC.
func normalizeResetEpoch(v *float64) *time.Time {
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

// attachRolloutOutcome wraps err with the outcome read from threadID's rollout
// under env's codex home. Any lookup/read failure returns err unchanged
// (=> classified "other"). Must run BEFORE SanitizeProcessError.
func attachRolloutOutcome(env []string, threadID string, err error, now time.Time) error {
	if err == nil || strings.TrimSpace(threadID) == "" {
		return err
	}
	home, herr := resolveCodexHome(env)
	if herr != nil {
		return err
	}
	path, ferr := findRollout(home, strings.TrimSpace(threadID))
	if ferr != nil {
		return err
	}
	f, oerr := os.Open(path)
	if oerr != nil {
		return err
	}
	defer f.Close()
	o, ok, rerr := readRolloutOutcome(f, now)
	if rerr != nil || !ok {
		return err
	}
	return pool.WithOutcome(err, o)
}

// rolloutOutcomeAttacher adapts attachRolloutOutcome to foldExecStream's
// classify hook; a resumed turn without thread.started uses the resume id.
func rolloutOutcomeAttacher(env []string, resumeID string) func(threadID string, err error) error {
	return func(threadID string, err error) error {
		if threadID == "" {
			threadID = strings.TrimSpace(resumeID)
		}
		return attachRolloutOutcome(env, threadID, err, time.Now())
	}
}

// threadIDFromLines returns the first thread.started id in exec stdout.
func threadIDFromLines(lines []string) string {
	for _, l := range lines {
		ev, ok, err := decodeLine(l)
		if err != nil || !ok {
			continue
		}
		if ev.Type == "thread.started" && ev.ThreadID != "" {
			return ev.ThreadID
		}
	}
	return ""
}
