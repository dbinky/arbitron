package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/adapter/procenv"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// bedrockProbe lists foundation models via the control-plane ListFoundationModels
// endpoint (GET https://bedrock.{region}.amazonaws.com/foundation-models),
// SigV4-signed with service "bedrock". REUSE the shared adapter/awssig signer +
// parser; do not add a second signer or parser (see plan note).
type bedrockProbe struct {
	httpClient       *http.Client
	timeout          func(context.Context) (context.Context, context.CancelFunc)
	maxResponseBytes int64
	clock            func() time.Time
	// endpoint derives the control-plane host for a region; a test-only seam so
	// the probe can be pointed at an httptest server. nil-safe default below.
	endpoint func(region string) string
}

func newBedrockProbe(d Deps) *bedrockProbe {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	clk := d.Clock
	if clk == nil {
		clk = time.Now
	}
	endpoint := d.BedrockEndpoint
	if endpoint == nil {
		endpoint = func(region string) string {
			return fmt.Sprintf("https://bedrock.%s.amazonaws.com/foundation-models", region)
		}
	}
	return &bedrockProbe{
		httpClient:       hc,
		timeout:          func(ctx context.Context) (context.Context, context.CancelFunc) { return procenv.WithTimeout(ctx, d.Timeout) },
		maxResponseBytes: d.MaxResponseBytes,
		clock:            clk,
		endpoint:         endpoint,
	}
}

var _ domprovider.ConnectionProbe = (*bedrockProbe)(nil)

// foundationModels is the ListFoundationModels body:
// {"modelSummaries":[{"modelId":"anthropic.claude-..."}]}.
type foundationModels struct {
	ModelSummaries []struct {
		ModelID string `json:"modelId"`
	} `json:"modelSummaries"`
}

func (p *bedrockProbe) Probe(ctx context.Context, b domprovider.ResolvedBinding) (domprovider.ProbeResult, error) {
	ctx, cancel := p.timeout(ctx)
	defer cancel()

	region := strings.TrimSpace(b.Extras["region"])
	accessKeyID := strings.TrimSpace(b.Extras["access-key-id"])
	if region == "" || accessKeyID == "" {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: bedrock requires region and access-key-id in extras", ErrRegionConfig)
	}
	// Reveal the sealed secret (bare string or {"secret","session_token"} JSON),
	// parsed by the SHARED awssig.ParseCredential.
	var cred awssig.Credential
	if b.RevealKey != nil {
		payload, kerr := b.RevealKey(ctx)
		if kerr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("probe: reveal key: %w", kerr)
		}
		var perr error
		cred, perr = awssig.ParseCredential(payload)
		if perr != nil {
			return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrRegionConfig, perr)
		}
	}

	// endpoint() is a test-only seam (default derives the real control-plane host).
	target := p.endpoint(region)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: build request: %w", err)
	}
	// awssig.Sign is the SHARED Phase-3 SigV4 signer (service "bedrock"); the
	// injected clock makes the signature deterministic in tests. It sets
	// Authorization, X-Amz-Date, and X-Amz-Security-Token (when SessionToken != "").
	if err := awssig.Sign(req, nil, accessKeyID, cred, region, "bedrock", p.clock()); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrRegionConfig, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := boundedReader(resp.Body, p.maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 403 from Bedrock is a credentials/SigV4 rejection -> credentials class.
		return domprovider.ProbeResult{}, classifyStatus(resp.StatusCode, reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: read response: %w", err)
	}
	var list foundationModels
	if err := json.Unmarshal(raw, &list); err != nil {
		return domprovider.ProbeResult{}, fmt.Errorf("probe: decode model list: %w", err)
	}
	ids := make([]string, 0, len(list.ModelSummaries))
	for _, m := range list.ModelSummaries {
		if m.ModelID != "" {
			ids = append(ids, m.ModelID)
		}
	}
	return domprovider.ProbeResult{Reachable: true, Models: ids}, nil
}
