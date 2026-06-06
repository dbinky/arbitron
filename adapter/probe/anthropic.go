package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// anthropicVersion is the pinned Messages-API version header. It MUST stay
// identical to adapter/anthropicapi.anthropicVersion (Phase 2) — same value
// "2023-06-01", changed only in lockstep. (A local const, not an import, because
// adapter/anthropicapi may be un-merged on this branch; keep the values equal.)
const anthropicVersion = "2023-06-01"

// defaultAnthropicBase is used when the connection omits a base_url.
const defaultAnthropicBase = "https://api.anthropic.com"

type anthropicProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
}

func newAnthropicProbe(d Deps) *anthropicProbe {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &anthropicProbe{
		httpClient:       hc,
		timeout:          func(ctx context.Context) (context.Context, context.CancelFunc) { return procenv.WithTimeout(ctx, d.Timeout) },
		maxResponseBytes: d.MaxResponseBytes,
	}
}

var _ domprovider.ConnectionProbe = (*anthropicProbe)(nil)

func (p *anthropicProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	base := b.BaseURL
	if strings.TrimSpace(base) == "" {
		base = defaultAnthropicBase
	}
	target := strings.TrimRight(base, "/") + "/v1/models"
	if err := checkScheme(target); err != nil {
		return domprovider.ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	// Anthropic always requires a key; a missing/empty key is a config fault we
	// let the upstream 401 surface as ErrCredentialsRejected.
	if b.RevealKey != nil {
		key, kerr := b.RevealKey(ctx)
		if kerr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("probe: reveal key: %w", kerr)
		}
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
	}
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
	// Anthropic list body: {"data":[{"id":"claude-..."}]}.
	var list modelsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode model list: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
