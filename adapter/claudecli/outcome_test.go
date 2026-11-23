package claudecli

import (
	"context"
	"errors"
	"testing"
	"time"

	pool "github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

func trackerFrom(t *testing.T, lines ...string) *outcomeTracker {
	t.Helper()
	var tr outcomeTracker
	for _, l := range lines {
		ev, ok, err := parseEvent([]byte(l))
		require.NoError(t, err)
		if ok {
			tr.observe(ev)
		}
	}
	return &tr
}

func TestParseEvent_DecodesRateLimitEvent(t *testing.T) {
	ev, ok, err := parseEvent([]byte(`{"type":"rate_limit_event","session_id":"s","uuid":"u","rate_limit_info":{"status":"rejected","resetsAt":1795262400,"rateLimitType":"five_hour"}}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, eventRateLimit, ev.Kind)
	require.NotNil(t, ev.Signal)
	require.NotNil(t, ev.Signal.Limit)
	assert.Equal(t, "rejected", ev.Signal.Limit.Status)
	assert.Equal(t, "five_hour", ev.Signal.Limit.RateLimitType)
	assert.Equal(t, 1795262400.0, *ev.Signal.Limit.ResetsAt)
}

func TestParseEvent_DecodesAssistantErrorKindAndQuota(t *testing.T) {
	ev, ok, err := parseEvent([]byte(`{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"quotaLimits":{"status":"rejected","resetsAt":1795262400,"rateLimitType":"seven_day"},"message":{"content":[{"type":"text","text":"You’ve hit your limit"}]}}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "You’ve hit your limit", ev.Text, "text still flows to the transcript")
	require.NotNil(t, ev.Signal)
	assert.Equal(t, "rate_limit", ev.Signal.ErrorKind)
	assert.Equal(t, 429, ev.Signal.APIErrorStatus)
	assert.Equal(t, "seven_day", ev.Signal.Limit.RateLimitType)
}

func TestParseEvent_ResultApiErrorStatus(t *testing.T) {
	ev, _, err := parseEvent([]byte(`{"type":"result","is_error":true,"api_error_status":529,"api_error_type":"overloaded_error","result":"x"}`))
	require.NoError(t, err)
	require.NotNil(t, ev.Signal)
	assert.Equal(t, 529, ev.Signal.APIErrorStatus)
}

// Malformed signal fields never fail the line (fail closed to "other").
func TestParseEvent_LenientSignalDecoding(t *testing.T) {
	ev, ok, err := parseEvent([]byte(`{"type":"assistant","error":{"nested":true},"apiErrorStatus":"429","quotaLimits":"garbage","message":{"content":[]}}`))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, ev.Signal)
}

func TestTrackerClassify(t *testing.T) {
	reset := time.Unix(1795262400, 0).UTC()
	cases := []struct {
		name   string
		lines  []string
		class  pool.OutcomeClass
		reset  *time.Time
		window *pool.LimitWindow
	}{
		{"authentication_failed", []string{`{"type":"assistant","error":"authentication_failed","message":{"content":[]}}`}, pool.OutcomeLoggedOut, nil, nil},
		{"oauth_org_not_allowed", []string{`{"type":"assistant","error":"oauth_org_not_allowed","message":{"content":[]}}`}, pool.OutcomeLoggedOut, nil, nil},
		{"billing_error", []string{`{"type":"assistant","error":"billing_error","message":{"content":[]}}`}, pool.OutcomeLoggedOut, nil, nil},
		{"401", []string{`{"type":"result","is_error":true,"api_error_status":401}`}, pool.OutcomeLoggedOut, nil, nil},
		{"overloaded", []string{`{"type":"assistant","error":"overloaded","message":{"content":[]}}`}, pool.OutcomeTransient, nil, nil},
		{"529", []string{`{"type":"result","is_error":true,"api_error_status":529}`}, pool.OutcomeTransient, nil, nil},
		{"server_error", []string{`{"type":"assistant","error":"server_error","message":{"content":[]}}`}, pool.OutcomeTransient, nil, nil},
		{"429 throttle without rejected quota", []string{
			`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}`,
			`{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"message":{"content":[]}}`,
		}, pool.OutcomeTransient, nil, nil},
		{"rejected quota seconds", []string{
			`{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"quotaLimits":{"status":"rejected","resetsAt":1795262400,"rateLimitType":"seven_day"},"message":{"content":[]}}`,
		}, pool.OutcomeExhausted, &reset, &pool.LimitWindow{LimitID: "seven_day", DurationMins: 10080, ResetAt: &reset}},
		{"rejected quota millis", []string{
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795262400000,"rateLimitType":"five_hour"}}`,
		}, pool.OutcomeExhausted, &reset, &pool.LimitWindow{LimitID: "five_hour", DurationMins: 300, ResetAt: &reset}},
		{"rejected per-model weekly", []string{
			`{"type":"assistant","error":"rate_limit","apiErrorParams":{"rate_limit_info":{"status":"rejected","resetsAt":1795262400,"rateLimitType":"seven_day_opus"}},"message":{"content":[]}}`,
		}, pool.OutcomeExhausted, &reset, &pool.LimitWindow{LimitID: "seven_day_opus", ModelScope: "opus", DurationMins: 10080, ResetAt: &reset}},
		{"rejected unknown reset", []string{
			`{"type":"assistant","error":"rate_limit","quotaLimits":{"status":"rejected","rateLimitType":"seven_day"},"message":{"content":[]}}`,
		}, pool.OutcomeExhausted, nil, &pool.LimitWindow{LimitID: "seven_day", DurationMins: 10080}},
		{"auth beats quota", []string{
			`{"type":"assistant","error":"authentication_failed","quotaLimits":{"status":"rejected","resetsAt":1795262400},"message":{"content":[]}}`,
		}, pool.OutcomeLoggedOut, nil, nil},
		{"unknown kind", []string{`{"type":"assistant","error":"model_not_found","message":{"content":[]}}`}, pool.OutcomeOther, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, ok := trackerFrom(t, tc.lines...).classify()
			require.True(t, ok)
			assert.Equal(t, tc.class, o.Class)
			if tc.reset == nil {
				assert.Nil(t, o.ResetAt)
			} else {
				require.NotNil(t, o.ResetAt)
				assert.True(t, tc.reset.Equal(*o.ResetAt))
			}
			if tc.window == nil {
				assert.Empty(t, o.Windows)
			} else {
				require.Len(t, o.Windows, 1)
				w := o.Windows[0]
				assert.Equal(t, tc.window.LimitID, w.LimitID)
				assert.Equal(t, tc.window.ModelScope, w.ModelScope)
				assert.Equal(t, tc.window.DurationMins, w.DurationMins)
				assert.Equal(t, tc.window.ResetAt == nil, w.ResetAt == nil)
			}
		})
	}
}

func TestTracker_NoSignalAttachesNothing(t *testing.T) {
	tr := trackerFrom(t, `{"type":"result","is_error":true,"result":"boom"}`)
	_, ok := tr.classify()
	assert.False(t, ok)
	raw := errors.New("x")
	assert.Same(t, raw, tr.attach(raw))
	assert.Nil(t, tr.attach(nil))
}

func TestTracker_HeadroomHint(t *testing.T) {
	var seen []pool.SourceOutcome
	ctx := pool.ContextWithOutcomeObserver(context.Background(), func(o pool.SourceOutcome) { seen = append(seen, o) })

	trackerFrom(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","utilization":0.85,"rateLimitType":"seven_day","resetsAt":1795262400}}`).reportSuccess(ctx)
	trackerFrom(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`).reportSuccess(ctx)
	trackerFrom(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","utilization":0.1}}`).reportSuccess(ctx)

	require.Len(t, seen, 2, "plain allowed is not a hint")
	assert.Equal(t, pool.OutcomeNone, seen[0].Class)
	assert.InDelta(t, 0.15, *seen[0].HeadroomHint, 1e-9)
	require.Len(t, seen[0].Windows, 1)
	assert.InDelta(t, 85.0, *seen[0].Windows[0].UsedPercent, 1e-9)
	assert.Nil(t, seen[1].HeadroomHint, "warning without utilization remains unknown")
}

func TestNormalizeEpoch(t *testing.T) {
	assert.Nil(t, normalizeEpoch(nil))
	assert.Nil(t, normalizeEpoch(f64(0)))
	assert.Nil(t, normalizeEpoch(f64(-5)))
	want := time.Unix(1795262400, 0).UTC()
	assert.True(t, want.Equal(*normalizeEpoch(f64(1795262400))))
	assert.True(t, want.Equal(*normalizeEpoch(f64(1795262400000))))
}
