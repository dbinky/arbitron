package provider_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretKey = "lr_live_Sup3rSecretValue_1234567890"

func TestClassifyLunaRoute_StatusMapping(t *testing.T) {
	cases := []struct {
		name string
		op   provider.LunaRouteOperation
		code int
		want error
	}{
		{"401 credentials", provider.LunaRouteChat, 401, provider.ErrLunaRouteCredentialsRejected},
		{"401 on listing", provider.LunaRouteListModels, 401, provider.ErrLunaRouteCredentialsRejected},
		{"429 busy", provider.LunaRouteChat, 429, provider.ErrLunaRouteBusy},
		{"500 failure", provider.LunaRouteChat, 500, provider.ErrLunaRouteServiceFailure},
		{"503 failure", provider.LunaRouteListModels, 503, provider.ErrLunaRouteServiceFailure},
		{"bare 404 listing is a failed listing", provider.LunaRouteListModels, 404, provider.ErrLunaRouteCauseUnknown},
		{"404 chat unknown", provider.LunaRouteChat, 404, provider.ErrLunaRouteCauseUnknown},
		{"403 unknown", provider.LunaRouteChat, 403, provider.ErrLunaRouteCauseUnknown},
		{"400 unknown", provider.LunaRouteChat, 400, provider.ErrLunaRouteCauseUnknown},
		{"418 unknown", provider.LunaRouteAgent, 418, provider.ErrLunaRouteCauseUnknown},
		{"302 unknown", provider.LunaRouteListModels, 302, provider.ErrLunaRouteCauseUnknown},
		{"499 unknown", provider.LunaRouteChat, 499, provider.ErrLunaRouteCauseUnknown},
		{"599 failure", provider.LunaRouteChat, 599, provider.ErrLunaRouteServiceFailure},
		{"600 unknown", provider.LunaRouteChat, 600, provider.ErrLunaRouteCauseUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: c.op, Status: c.code, Body: "x"})
			require.NotNil(t, f)
			assert.ErrorIs(t, f, c.want)
			assert.Equal(t, c.code, f.Status)
			assert.Equal(t, c.op, f.Operation)
			for _, other := range provider.LunaRouteGatewaySentinels() {
				if other != c.want {
					assert.NotErrorIs(t, f, other)
				}
			}
		})
	}
}

func TestClassifyLunaRoute_UnverifiedCodeIsNotGuessed(t *testing.T) {
	for _, op := range []provider.LunaRouteOperation{provider.LunaRouteChat, provider.LunaRouteListModels, provider.LunaRouteAgent} {
		for _, code := range []string{"MODEL_NOT_FOUND", "MODEL_INCOMPATIBLE", "invalid_api_key"} {
			f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: op, Status: 400, Code: code})
			assert.ErrorIs(t, f, provider.ErrLunaRouteCauseUnknown)
			assert.Empty(t, f.Snippet)
		}
		f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: op, Status: 429, Code: "lane_busy"})
		assert.ErrorIs(t, f, provider.ErrLunaRouteBusy)
	}
}

func TestClassifyLunaRoute_TransportErrors(t *testing.T) {
	tlsErr := errors.New("tls: handshake failure talking to gw.lunaroute.com")
	for _, original := range []error{tlsErr, context.DeadlineExceeded, fmt.Errorf("call: %w", context.Canceled)} {
		f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Transport: original, Status: 401, Code: "INVALID_API_KEY"})
		want := provider.ErrLunaRouteServiceFailure
		if errors.Is(original, context.Canceled) {
			want = provider.ErrLunaRouteCauseUnknown
		}
		assert.ErrorIs(t, f, want)
		assert.ErrorIs(t, f, original)
		assert.Contains(t, f.Error(), original.Error())
	}
}

func TestClassifyLunaRoute_MalformedListingIsServiceFailure(t *testing.T) {
	bad := errors.New("invalid character '<' looking for beginning of value")
	f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: 200, Malformed: bad, Code: "INVALID_API_KEY"})
	assert.ErrorIs(t, f, provider.ErrLunaRouteServiceFailure)
	assert.ErrorIs(t, f, bad)
	assert.Contains(t, f.Error(), "malformed response")
}

func TestClassifyLunaRoute_RedactsKeysEverywhere(t *testing.T) {
	transport := &lunaRouteSecretError{text: "dial: Authorization: Bearer " + secretKey}
	body := `{"error":"bad key ` + secretKey + `","other":"lr_someOtherKey99"}`
	f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 401, Body: body, Transport: transport, Secrets: []string{secretKey}})
	msg := f.Error()
	assert.NotContains(t, msg, secretKey)
	assert.NotContains(t, msg, "Sup3rSecret")
	assert.NotContains(t, msg, "lr_someOtherKey99")
	assert.Empty(t, f.Snippet)
	assert.Contains(t, msg, "[REDACTED]")
	assert.ErrorIs(t, f, transport)
	var typed *lunaRouteSecretError
	require.ErrorAs(t, f, &typed)
	assert.Same(t, transport, typed)
	bad := errors.New("decode " + secretKey + " Bearer foreign-token")
	f = provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Malformed: bad, Secrets: []string{secretKey}})
	assert.ErrorIs(t, f, bad)
	assert.NotContains(t, f.Error(), secretKey)
	assert.NotContains(t, f.Error(), "foreign-token")
}

func TestRedactLunaRoute_BeforeTruncation(t *testing.T) {
	body := strings.Repeat("a", 240) + secretKey + strings.Repeat("b", 100)
	out := provider.RedactLunaRoute(body, secretKey)
	assert.NotEmpty(t, out)
	assert.LessOrEqual(t, len(out), 256)
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "lr_live")
	assert.NotContains(t, out, "Sup3r")
	// Arbitrary revealed secrets, not just lr_ keys, must be removed first.
	out = provider.RedactLunaRoute(strings.Repeat("a", 240)+"foreign-key-secret-value"+strings.Repeat("b", 100), "foreign-key-secret-value")
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "foreign")
}

func TestRedactLunaRoute_ByteLimit(t *testing.T) {
	for _, size := range []int{256, 257} {
		t.Run(fmt.Sprintf("bytes=%d", size), func(t *testing.T) {
			assert.Equal(t, strings.Repeat("a", 256), provider.RedactLunaRoute(strings.Repeat("a", size)))
		})
	}
}

func TestRedactLunaRoute_ScrubsControlsAndBearer(t *testing.T) {
	out := provider.RedactLunaRoute("line1\nline2\x00 Bearer sk-or-abc123 x-api-key: lr_abc", "")
	assert.Contains(t, out, "line1 line2 ")
	assert.NotContains(t, out, "\n")
	assert.NotContains(t, out, "\x00")
	assert.NotContains(t, out, "sk-or-abc123")
	assert.NotContains(t, out, "lr_abc")
	for _, auth := range []string{"bEaReR foreign-token", "X-API-Key = foreign-token", "lunaroute-api-key: foreign-token"} {
		assert.NotContains(t, provider.RedactLunaRoute(auth), "foreign-token")
	}
	assert.Equal(t, "[REDACTED]", provider.RedactLunaRoute("abcdef", "abc", "abcdef"), "overlapping secrets must not leave a suffix")
	assert.Equal(t, "x y z", provider.RedactLunaRoute("x\x7fy\u0085z"))
	out = provider.RedactLunaRoute(strings.Repeat("a", 255) + "界")
	assert.Len(t, out, 255)
	assert.True(t, utf8.ValidString(out))
	assert.Equal(t, "diagnostic", provider.RedactLunaRoute("  diagnostic  ", ""))
}

func TestLunaRouteFailure_ErrorShape(t *testing.T) {
	f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: 503, Body: "upstream down"})
	assert.Contains(t, f.Error(), "lunaroute list models")
	assert.Contains(t, f.Error(), "HTTP 503")
	assert.NotContains(t, f.Error(), "upstream down")
	assert.Contains(t, f.Error(), "retry later")
	for _, body := range []string{"public upstream diagnostic", strings.Repeat("x", 240) + secretKey[:15], `{"error":{"code":"INVALID_API_KEY"},"message":"private"}`} {
		f = provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 400, Body: body})
		assert.Empty(t, f.Snippet, "raw/possibly cut body is never diagnostic text")
		assert.NotContains(t, f.Error(), body)
	}
	f = provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 400, Code: "INVALID_API_KEY", Body: "public upstream diagnostic"})
	assert.Equal(t, "code=INVALID_API_KEY", f.Snippet)
	assert.Contains(t, f.Error(), "code=INVALID_API_KEY")
	assert.NotContains(t, f.Error(), "public upstream diagnostic")
}

func TestLunaRouteErrorCode_Envelopes(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"nested", `{"error":{"code":"INVALID_API_KEY"}}`, "INVALID_API_KEY"},
		{"wrapped", `{"type":"error","error":{"code":"MODEL_UNPRICED"}}`, "MODEL_UNPRICED"},
		{"bare", `{"code":"BYOK_SPEND_CAP_REACHED"}`, "BYOK_SPEND_CAP_REACHED"},
		{"matching", `{"code":"INVALID_API_KEY","error":{"code":"INVALID_API_KEY"}}`, "INVALID_API_KEY"},
		{"contradictory", `{"code":"INVALID_API_KEY","error":{"code":"MODEL_UNPRICED"}}`, ""},
		{"nested missing beats top", `{"code":"INVALID_API_KEY","error":{}}`, ""},
		{"nested null beats top", `{"code":"INVALID_API_KEY","error":{"code":null}}`, ""},
		{"null error", `{"error":null,"code":"INVALID_API_KEY"}`, ""},
		{"string error", `{"error":"INVALID_API_KEY"}`, ""},
		{"number code", `{"error":{"code":401}}`, ""},
		{"wrong case nested field", `{"error":{"CODE":"INVALID_API_KEY"}}`, ""},
		{"wrong case top field", `{"Code":"INVALID_API_KEY"}`, ""},
		{"bare null", `{"code":null}`, ""},
		{"bare nonstring", `{"code":true}`, ""},
		{"missing", `{}`, ""},
		{"message not authoritative", `{"error":{"type":"INVALID_API_KEY","message":"MODEL_NOT_FOUND"}}`, ""},
		{"unknown", `{"error":{"code":"MODEL_NOT_FOUND"}}`, "MODEL_NOT_FOUND"},
		{"malformed", `{"error":{"code":"INVALID_API_KEY"}`, ""},
		{"trailing JSON", `{"code":"INVALID_API_KEY"} {}`, ""},
		{"array", `[{"code":"INVALID_API_KEY"}]`, ""},
		{"null", `null`, ""},
	}
	for _, op := range []provider.LunaRouteOperation{provider.LunaRouteChat, provider.LunaRouteListModels, provider.LunaRouteAgent} {
		for _, c := range cases {
			t.Run(string(op)+"/"+c.name, func(t *testing.T) {
				assert.Equal(t, c.want, provider.LunaRouteErrorCode(op, c.body))
			})
		}
	}
	assert.Empty(t, provider.LunaRouteErrorCode("unsupported", `{"code":"INVALID_API_KEY"}`))
}

func TestNormalizeLunaRouteRetryAfter(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"0", "0"}, {"00120", "120"}, {" 60 ", "60"},
		{"9", "9"}, {"19", "19"},
		{"Wed, 21 Oct 2015 07:28:00 GMT", "Wed, 21 Oct 2015 07:28:00 GMT"},
		{"", ""}, {"-1", ""}, {"+1", ""}, {"1.5", ""},
		{"18446744073709551616", ""}, {"60\r\nX-Key: " + secretKey, ""}, {"60\n", ""},
		{"tomorrow", ""}, {"Bearer foreign-token", ""}, {"Wed, 99 Oct 2015 07:28:00 GMT", ""},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) { assert.Equal(t, c.want, provider.NormalizeLunaRouteRetryAfter(c.raw)) })
	}
}

func TestClassifyLunaRoute_RetryGuidance(t *testing.T) {
	for _, raw := range []string{"120", "Wed, 21 Oct 2015 07:28:00 GMT"} {
		f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 429, RetryAfter: raw})
		assert.Equal(t, raw, f.RetryAfter)
		assert.Contains(t, f.Error(), "; retry after "+raw)
	}
	for _, code := range []string{"MODEL_UNPRICED", "BYOK_SPEND_CAP_REACHED"} {
		f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 429, Code: code, RetryAfter: "120"})
		assert.ErrorIs(t, f, provider.ErrLunaRouteRequestRefused)
		assert.NotErrorIs(t, f, provider.ErrLunaRouteBusy)
		assert.Empty(t, f.RetryAfter)
		assert.NotContains(t, f.Error(), "retry after")
		assert.NotContains(t, f.Error(), "retry later")
	}
	f := provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 503, RetryAfter: "120"})
	assert.Empty(t, f.RetryAfter)
	f = provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 400, Code: "CONCURRENT_REQUEST_LIMIT_EXCEEDED", RetryAfter: "00120"})
	assert.Equal(t, "120", f.RetryAfter)
	for _, raw := range []string{secretKey, "Bearer foreign-token", "120\r\n" + secretKey} {
		f = provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: 429, RetryAfter: raw})
		assert.Empty(t, f.RetryAfter)
		assert.NotContains(t, f.Error(), raw)
	}
}
