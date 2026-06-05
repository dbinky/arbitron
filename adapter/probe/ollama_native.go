package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// tagsList is Ollama's native list body: {"models":[{"name":"llama3:8b"}]}.
type tagsList struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// probeOllamaNative lists models via GET {base}/api/tags. The base may already
// carry an OpenAI-compat /v1 suffix, so /v1 is trimmed before appending /api/tags.
func (p *openAICompatProbe) probeOllamaNative(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	base := strings.TrimRight(b.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	target := base + "/api/tags"
	if err := checkScheme(target); err != nil {
		return domprovider.ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
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
	var list tagsList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode tags list: %w", err)
	}
	ids := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		if m.Name != "" {
			ids = append(ids, m.Name)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
