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

// openAICompatProbe lists models from any OpenAI-compatible endpoint via
// GET {base}/models with Bearer auth, falling back to Ollama's native /api/tags
// when /models is absent (404).
type openAICompatProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
}

func newOpenAICompatProbe(d Deps) *openAICompatProbe {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &openAICompatProbe{
		httpClient: hc,
		timeout: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return procenv.WithTimeout(ctx, d.Timeout)
		},
		maxResponseBytes: d.MaxResponseBytes,
	}
}

var _ domprovider.ConnectionProbe = (*openAICompatProbe)(nil)

// modelsList is the OpenAI-shaped list body: {"data":[{"id":"..."}]}.
type modelsList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (p *openAICompatProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	target := strings.TrimRight(b.BaseURL, "/") + "/models"
	if err := checkScheme(target); err != nil {
		return domprovider.ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	if b.RevealKey != nil {
		key, kerr := b.RevealKey(ctx)
		if kerr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("probe: reveal key: %w", kerr)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)

	// Ollama-native fallback when the OpenAI-compat /models path is absent.
	if resp.StatusCode == http.StatusNotFound {
		return p.probeOllamaNative(ctx, b)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
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
