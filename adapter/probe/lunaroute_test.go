package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrProbeKey = "lr_live_probe_SECRET_123"

// Wrong path or auth cannot return a successful listing. The transport answers
// at the hosted URL; no test-only endpoint exception exists in production.
type lrGateway struct {
	reqs    []*http.Request
	status  int
	body    string
	header  http.Header
	err     error
	readErr error
}

func (g *lrGateway) RoundTrip(req *http.Request) (*http.Response, error) {
	g.reqs = append(g.reqs, req)
	if g.err != nil {
		return nil, g.err
	}
	if req.URL.Scheme != "https" || req.URL.Host != "gw.lunaroute.com" {
		return nil, errors.New("fake gateway: wrong host")
	}
	status, body := g.status, g.body
	if req.Method != http.MethodGet || req.URL.RequestURI() != "/v1/models" {
		status, body = 404, `{"error":"no route"}`
	} else if req.Header.Get("Authorization") != "Bearer "+lrProbeKey {
		status, body = 401, `{"error":{"code":"INVALID_API_KEY"}}`
	}
	var reader io.Reader = strings.NewReader(body)
	if g.readErr != nil {
		reader = io.MultiReader(reader, lrErrorReader{g.readErr})
	}
	return &http.Response{StatusCode: status, Header: g.header, Body: io.NopCloser(reader), Request: req}, nil
}

type lrErrorReader struct{ err error }

func (r lrErrorReader) Read([]byte) (int, error) { return 0, r.err }

func lrProbeBinding(base, key string) domprovider.ResolvedBinding {
	return domprovider.ResolvedBinding{Provider: "lunaroute", BaseURL: base, ConnectionRef: "lr",
		RevealKey: func(context.Context) (string, error) { return key, nil }}
}

func newLRProbe(g *lrGateway, max int64) *lunaRouteProbe {
	return newLunaRouteProbe(Deps{HTTPClient: &http.Client{Transport: g}, MaxResponseBytes: max})
}

func TestLunaRouteProbe_ListsModels(t *testing.T) {
	for _, base := range []string{"https://gw.lunaroute.com", "https://gw.lunaroute.com/", "https://gw.lunaroute.com/v1", "https://gw.lunaroute.com/v1/", ""} {
		t.Run(base, func(t *testing.T) {
			g := &lrGateway{status: 200, body: `{"object":"list","data":[{"id":"glm-5.3","context_length":1},{"id":""},{"id":"claude-sonnet-5-lr1"}]}`}
			res, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding(base, lrProbeKey))
			require.NoError(t, err)
			assert.True(t, res.Reachable)
			assert.False(t, res.ModelsUnsupported)
			assert.Equal(t, []string{"glm-5.3", "claude-sonnet-5-lr1"}, res.Models)
			require.Len(t, g.reqs, 1)
			r := g.reqs[0]
			assert.Equal(t, "https://gw.lunaroute.com/v1/models", r.URL.String())
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer "+lrProbeKey, r.Header.Get("Authorization"))
			for _, name := range []string{"x-api-key", "LUNAROUTE-API-KEY", "anthropic-version", "X-LunaRoute-Client", "LUNAROUTE-PROJECT-ID"} {
				assert.Empty(t, r.Header.Get(name), name)
			}
		})
	}
}

func TestLunaRouteProbe_EmptyListIsReachable(t *testing.T) {
	g := &lrGateway{status: 200, body: `{"data":[]}`}
	res, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.Empty(t, res.Models)
}

func TestLunaRouteProbe_StatusClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{401, domprovider.ErrLunaRouteCredentialsRejected}, {404, domprovider.ErrLunaRouteCauseUnknown},
		{429, domprovider.ErrLunaRouteBusy}, {503, domprovider.ErrLunaRouteServiceFailure}, {418, domprovider.ErrLunaRouteCauseUnknown},
	} {
		g := &lrGateway{status: tc.status, body: `{"error":"echo ` + lrProbeKey + `"}`, header: http.Header{"Retry-After": {"23"}}}
		_, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
		require.ErrorIs(t, err, tc.want)
		var f *domprovider.LunaRouteFailure
		require.ErrorAs(t, err, &f)
		assert.Equal(t, domprovider.LunaRouteListModels, f.Operation)
		assert.Equal(t, tc.status, f.Status)
		if tc.status == 429 {
			assert.Equal(t, "23", f.RetryAfter)
		} else {
			assert.Empty(t, f.RetryAfter)
		}
		assert.NotContains(t, err.Error(), lrProbeKey)
		assert.NotErrorIs(t, err, ErrCredentialsRejected)
	}
}

func TestLunaRouteProbe_VerifiedCodeFromHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"bare", `{"code":"INVALID_API_KEY"}`, 400, domprovider.ErrLunaRouteCredentialsRejected},
		{"wrapped", `{"type":"error","error":{"code":"NO_HEALTHY_PROVIDER"}}`, 400, domprovider.ErrLunaRouteServiceFailure},
		{"unpriced", `{"error":{"code":"MODEL_UNPRICED"}}`, 429, domprovider.ErrLunaRouteRequestRefused},
		{"spend-cap", `{"code":"BYOK_SPEND_CAP_REACHED"}`, 429, domprovider.ErrLunaRouteRequestRefused},
		{"undocumented", `{"code":"MODEL_NOT_FOUND"}`, 404, domprovider.ErrLunaRouteCauseUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &lrGateway{status: tc.status, body: tc.body, header: http.Header{"Retry-After": {"17"}}}
			b := lrProbeBinding("", lrProbeKey)
			p, err := NewProbeFactory(Deps{HTTPClient: &http.Client{Transport: g}}).ProbeFor(b)
			require.NoError(t, err)
			_, err = p.Probe(context.Background(), b)
			require.ErrorIs(t, err, tc.want)
			var f *domprovider.LunaRouteFailure
			require.ErrorAs(t, err, &f)
			assert.Equal(t, domprovider.LunaRouteListModels, f.Operation)
			assert.Empty(t, f.RetryAfter, "non-busy errors must not encourage retry")
			require.Len(t, g.reqs, 1)
			assert.Empty(t, g.reqs[0].Header.Get("anthropic-version"))
			assert.Empty(t, g.reqs[0].Header.Get("X-LunaRoute-Client"))
		})
	}
}

func TestLunaRouteProbe_WrongKeyIs401FromGateway(t *testing.T) {
	g := &lrGateway{status: 200, body: `{"data":[]}`}
	_, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", "lr_other_key"))
	require.ErrorIs(t, err, domprovider.ErrLunaRouteCredentialsRejected)
	require.Len(t, g.reqs, 1)
}

func TestLunaRouteProbe_TransportFailure(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, cause := range []error{
		errors.New("tls: handshake failure " + lrProbeKey),
		&tls.CertificateVerificationError{Err: errors.New("certificate rejected " + lrProbeKey)},
		context.Canceled,
	} {
		g := &lrGateway{err: cause}
		_, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
		require.ErrorIs(t, err, cause)
		if errors.Is(cause, context.Canceled) {
			require.ErrorIs(t, err, domprovider.ErrLunaRouteCauseUnknown)
		} else {
			require.ErrorIs(t, err, domprovider.ErrLunaRouteServiceFailure)
		}
		assert.NotContains(t, err.Error(), lrProbeKey)
		assert.Len(t, g.reqs, 1, "no retries or diagnosis requests")
	}
	assert.NotContains(t, logs.String(), lrProbeKey)
}

func TestLunaRouteProbe_CancellationAndTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Millisecond} {
		ctx, cancel := context.WithCancel(context.Background())
		if timeout == 0 {
			cancel()
		}
		tr := lrContextTransport{}
		p := newLunaRouteProbe(Deps{HTTPClient: &http.Client{Transport: tr}, Timeout: timeout})
		_, err := p.Probe(ctx, lrProbeBinding("", lrProbeKey))
		cancel()
		if timeout == 0 {
			require.ErrorIs(t, err, context.Canceled)
		} else {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		}
		assert.NotContains(t, err.Error(), lrProbeKey)
	}
}

type lrContextTransport struct{}

func (lrContextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestLunaRouteProbe_MalformedListingIsNeverAnEmptyCatalog(t *testing.T) {
	for _, body := range []string{
		`<html>oops</html>`, `{"object":"list"}`, `{"data":"nope"}`, `null`, `{"data":null}`, `{"data":[null]}`,
		`{"data":[{}]}`, `{"data":[{"id":null}]}`, `{"data":[{"id":12}]}`, `{"data":[{"id":true}]}`, `{"data":["glm-5.3"]}`, `{"data":[]} trailing`,
	} {
		t.Run(body, func(t *testing.T) {
			g := &lrGateway{status: 200, body: body}
			res, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
			require.ErrorIs(t, err, domprovider.ErrLunaRouteServiceFailure)
			assert.False(t, res.Reachable)
			assert.Nil(t, res.Models)
		})
	}
}

func TestLunaRouteProbe_OversizedBody(t *testing.T) {
	g := &lrGateway{status: 200, body: `{"data":[{"id":"` + strings.Repeat("x", 4096) + `"}]}`}
	_, err := newLRProbe(g, 64).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
	require.ErrorIs(t, err, procenv.ErrResponseTooLarge)
	require.ErrorIs(t, err, domprovider.ErrLunaRouteServiceFailure)
}

func TestLunaRouteProbe_ReadDiagnosticsDropIncompleteBodies(t *testing.T) {
	for _, status := range []int{200, 401, 429} {
		for _, oversized := range []bool{false, true} {
			// The complete-looking code must not override status after a read error.
			body := `{"code":"INVALID_API_KEY","echo":"` + lrProbeKey + `"}`
			g := &lrGateway{status: status, body: body}
			var max int64
			var cause error = errors.New("read failed " + lrProbeKey)
			if oversized {
				max = int64(len(body) - 8)
				cause = procenv.ErrResponseTooLarge
			} else {
				g.readErr = cause
			}
			res, err := newLRProbe(g, max).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
			require.ErrorIs(t, err, cause)
			want := domprovider.ErrLunaRouteServiceFailure
			if status == 401 {
				want = domprovider.ErrLunaRouteCredentialsRejected
			}
			if status == 429 {
				want = domprovider.ErrLunaRouteBusy
			}
			require.ErrorIs(t, err, want)
			var f *domprovider.LunaRouteFailure
			require.ErrorAs(t, err, &f)
			assert.Empty(t, f.Snippet)
			assert.NotContains(t, err.Error(), lrProbeKey)
			assert.NotContains(t, err.Error(), "INVALID_API_KEY")
			assert.False(t, res.Reachable)
		}
	}
}

func TestLunaRouteProbe_RedirectNotFollowed(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		g := &lrGateway{status: status, header: http.Header{"Location": {"https://evil.example/v1/models"}}}
		_, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", lrProbeKey))
		require.ErrorIs(t, err, domprovider.ErrLunaRouteCauseUnknown)
		assert.Len(t, g.reqs, 1, "credential never replayed to Location")
	}
}

func TestLunaRouteProbe_GuardsFailBeforeAnyRequest(t *testing.T) {
	g := &lrGateway{status: 200, body: `{"data":[]}`}
	_, err := newLRProbe(g, 0).Probe(context.Background(), lrProbeBinding("", "sk-or-v1-foreign"))
	require.ErrorIs(t, err, domprovider.ErrLunaRouteStoredCredential)
	b := lrProbeBinding("https://evil.example/v1", lrProbeKey)
	b.RevealKey = func(context.Context) (string, error) { t.Fatal("invalid endpoint must not reveal"); return "", nil }
	_, err = newLRProbe(g, 0).Probe(context.Background(), b)
	require.ErrorIs(t, err, domprovider.ErrLunaRouteEndpoint)
	revealErr := errors.New("decrypt failed " + lrProbeKey)
	b = lrProbeBinding("", "")
	b.RevealKey = func(context.Context) (string, error) { return "", revealErr }
	_, err = newLRProbe(g, 0).Probe(context.Background(), b)
	require.ErrorIs(t, err, revealErr)
	assert.NotContains(t, err.Error(), lrProbeKey)
	b.RevealKey = nil
	_, err = newLRProbe(g, 0).Probe(context.Background(), b)
	require.ErrorIs(t, err, domprovider.ErrLunaRouteStoredCredential)
	assert.Empty(t, g.reqs)
}

func TestProbeFactory_ProbeFor_LunaRoute(t *testing.T) {
	p, err := NewProbeFactory(Deps{}).ProbeFor(domprovider.ResolvedBinding{Provider: " LunaRoute "})
	require.NoError(t, err)
	assert.IsType(t, (*lunaRouteProbe)(nil), p)
}
