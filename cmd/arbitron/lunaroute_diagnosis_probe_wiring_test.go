package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/arbitron/arbitron/domain/instance"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paired:6 (Area 2 diagnosis ↔ Area 7 discovery probe). The production root
// runs a pinned codex profile that fails opaquely; the real probe selector then
// lists /v1/models once, and the gateway answers in every shape the ProbeResult
// contract must distinguish. The sink is the persisted node error.
//
// It breaks if a truncated, cut-off or non-JSON listing is read as an
// authoritative partial listing ("model not available now"), or if a 401/503
// verdict reaches the wrapper as a plain error ("could not be checked").
func TestLunaRouteCodexWiringDiagnosisReadsProbeContract(t *testing.T) {
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds, argv, _, fail, _, _ := codexRoot(t, "glm-5.3", gw)
	require.NoError(t, os.WriteFile(fail, nil, 0o600))

	body := func(status int, src io.Reader) func(*http.Request) *http.Response {
		return func(r *http.Request) *http.Response {
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(src), Request: r}
		}
	}
	text := func(status int, s string) func() func(*http.Request) *http.Response {
		return func() func(*http.Request) *http.Response { return body(status, strings.NewReader(s)) }
	}
	const (
		observed  = "a current observation, not proof of the original cause"
		unchecked = "could not be checked afterwards"
		listedNow = "the credential was accepted and the model is listed now"
		malformed = "malformed response"
	)
	unavailable := domprovider.ErrLunaRouteModelUnavailable.Error()
	service := domprovider.ErrLunaRouteServiceFailure.Error()
	rejected := domprovider.ErrLunaRouteCredentialsRejected.Error()

	for _, tc := range []struct {
		name    string
		answer  func() func(*http.Request) *http.Response
		want    []string
		notWant []string
	}{
		{"200 listing the model", text(200, `{"object":"list","data":[{"id":"other"},{"id":"glm-5.3"}]}`),
			[]string{listedNow}, []string{unavailable, rejected, service}},
		{"200 without the model", text(200, `{"object":"list","data":[{"id":"other"}]}`),
			[]string{unavailable}, []string{listedNow, rejected, service}},
		{"200 empty list", text(200, `{"object":"list","data":[]}`),
			[]string{unavailable}, []string{listedNow, rejected, service}},
		{"200 body cut mid-listing", text(200, `{"object":"list","data":[{"id":"other"},{"id":"glm-`),
			[]string{service, malformed}, []string{unavailable, listedNow}},
		{"200 complete prefix then read error", func() func(*http.Request) *http.Response {
			return body(200, io.MultiReader(strings.NewReader(`{"object":"list","data":[{"id":"other"}]}`), iotest.ErrReader(io.ErrUnexpectedEOF)))
		}, []string{service, malformed}, []string{unavailable, listedNow}},
		{"200 non-JSON", text(200, `<html>gateway maintenance</html>`),
			[]string{service, malformed}, []string{unavailable, listedNow}},
		{"200 without data array", text(200, `{"object":"list"}`),
			[]string{service, malformed}, []string{unavailable, listedNow}},
		{"401", text(401, `{"error":"no"}`),
			[]string{rejected, "(HTTP 401)"}, []string{unavailable, listedNow}},
		{"503 then read error", func() func(*http.Request) *http.Response {
			return body(503, io.MultiReader(strings.NewReader(`{"error":"do`), iotest.ErrReader(io.ErrUnexpectedEOF)))
		}, []string{service, "(HTTP 503)"}, []string{unavailable, listedNow}},
		{"503", text(503, `{"error":"down"}`),
			[]string{service, "(HTTP 503)"}, []string{unavailable, listedNow, malformed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw.setAnswer(tc.answer())
			before := len(gw.seen())
			runsBefore := countLines(t, argv)
			id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-codex", nil, "normal")
			require.NoError(t, err)
			inst := waitTerminal(t, ds, id)
			require.Equal(t, instance.StateFailed, inst.State)
			msg := nodeError(t, ds, id)
			assert.Contains(t, msg, "harness process failed", "the original failure is kept")
			assert.Contains(t, msg, observed, "every gateway answer is a checked observation")
			assert.NotContains(t, msg, unchecked)
			for _, s := range tc.want {
				assert.Contains(t, msg, s)
			}
			for _, s := range tc.notWant {
				assert.NotContains(t, msg, s)
			}
			assert.NotContains(t, msg, lrRootKey)
			assert.Equal(t, []string{"GET https://gw.lunaroute.com/v1/models auth=ok"}, gw.seen()[before:], "exactly one probe per failure")
			assert.Equal(t, runsBefore+1, countLines(t, argv), "diagnosis never retries the mission")
		})
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(raw), "\n")
}
