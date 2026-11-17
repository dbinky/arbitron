package probe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// lunaRouteProbe lists available ids only. It enforces the hosted endpoint and
// credential guards even for callers that bypass profile resolution.
type lunaRouteProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
}

var _ domprovider.ConnectionProbe = (*lunaRouteProbe)(nil)

func newLunaRouteProbe(d Deps) *lunaRouteProbe {
	return &lunaRouteProbe{
		httpClient: procenv.NoRedirectClient(d.HTTPClient),
		timeout: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return procenv.WithTimeout(ctx, d.Timeout)
		},
		maxResponseBytes: d.MaxResponseBytes,
	}
}

// Pointers distinguish missing/null fields from an explicit empty array/id.
// LR-04: only data[].id is consumed, never capability metadata.
type lunaRouteModelList struct {
	Data *[]*struct {
		ID *string `json:"id"`
	} `json:"data"`
}

func (p *lunaRouteProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	base, err := domprovider.LunaRouteOpenAIBaseURL(b.BaseURL)
	if err != nil {
		return domprovider.ProbeResult{}, err
	}
	key, err := domprovider.GuardLunaRouteReveal(b).RevealKey(ctx)
	if err != nil {
		return domprovider.ProbeResult{}, err
	}
	var retryAfter string
	outcome := func(status int, body string, transport, malformed error) error {
		return domprovider.ClassifyLunaRoute(domprovider.LunaRouteOutcome{
			Operation: domprovider.LunaRouteListModels, Status: status, Body: body,
			Code:      domprovider.LunaRouteErrorCode(domprovider.LunaRouteListModels, body),
			Transport: transport, Malformed: malformed, RetryAfter: retryAfter, Secrets: []string{key},
		})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return domprovider.ProbeResult{}, outcome(0, "", err, nil)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, outcome(0, "", err, nil)
	}
	defer func() { _ = resp.Body.Close() }()
	retryAfter = resp.Header.Get("Retry-After")
	raw, readErr := io.ReadAll(boundedReader(resp.Body, p.maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if readErr != nil {
			// An incomplete body cannot supply a trusted code or safe snippet.
			// Preserve status classification and read-error identity, not raw text.
			classified := outcome(resp.StatusCode, "", nil, nil)
			return domprovider.ProbeResult{}, domprovider.SanitizeLunaRouteError(errors.Join(classified, readErr), key)
		}
		return domprovider.ProbeResult{}, outcome(resp.StatusCode, string(raw), nil, nil)
	}
	if readErr != nil {
		return domprovider.ProbeResult{}, outcome(resp.StatusCode, "", nil, readErr)
	}
	var list lunaRouteModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, outcome(resp.StatusCode, "", nil, err)
	}
	if list.Data == nil {
		return domprovider.ProbeResult{}, outcome(resp.StatusCode, "", nil, errors.New("model listing has no data array"))
	}
	ids := make([]string, 0, len(*list.Data))
	for _, m := range *list.Data {
		if m == nil || m.ID == nil {
			return domprovider.ProbeResult{}, outcome(resp.StatusCode, "", nil, errors.New("model row requires a string id"))
		}
		if *m.ID != "" {
			ids = append(ids, *m.ID)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
