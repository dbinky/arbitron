package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/arbitron/arbitron/domain/modelcatalog"
)

var _ modelcatalog.CatalogFetcher = (*Fetcher)(nil)

const (
	maxCatalogBytes     = 20 << 20
	fetchTimeout        = 30 * time.Second
	maxCatalogRedirects = 3
)

// Fetcher bounds downloads and rejects unsafe outbound destinations. Catalog
// content decides which models get paid probes, so the source and every
// redirect must be https: plaintext would let a network attacker rewrite it.
// The client is private; tests may substitute only its I/O leaf.
type Fetcher struct {
	client *http.Client
	// allowHTTP is a package-test opt-in for plaintext httptest fixtures.
	allowHTTP bool
}

func NewFetcher() *Fetcher {
	return NewFetcherWithTransport(nil)
}

// NewFetcherWithTransport substitutes HTTP I/O for composition-root tests.
// A nil transport uses the production DNS/dial guard. URL/redirect validation
// and download bounds remain in place even with an explicit transport.
func NewFetcherWithTransport(transport http.RoundTripper) *Fetcher {
	return &Fetcher{client: outbound.NewGuardedHTTPSClientWithTransport(fetchTimeout, maxCatalogRedirects, transport)}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fetchFailure("build request", err)
	}
	if !f.allowHTTP {
		if err := outbound.RequireHTTPS(req.URL); err != nil {
			return nil, fetchFailure("build request", err)
		}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fetchFailure("fetch", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("catalog: HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxCatalogBytes {
		return nil, fmt.Errorf("catalog: body exceeds %d bytes", maxCatalogBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, fetchFailure("read body", err)
	}
	if len(body) > maxCatalogBytes {
		return nil, fmt.Errorf("catalog: body exceeds %d bytes", maxCatalogBytes)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("catalog: empty body")
	}
	return body, nil
}

// HTTP errors can contain both the requested URL and a redirect's Location,
// including credentials/query tokens, even inside nested parse or read errors.
// Keep only safe classifications: never retain the raw error in the chain that
// the refresh pipeline logs. Timeouts and cancellation remain machine-readable.
func fetchFailure(operation string, err error) error {
	for _, cause := range []error{
		context.Canceled, context.DeadlineExceeded,
		outbound.ErrUnsafeURL, outbound.ErrBlockedDialTarget, io.ErrUnexpectedEOF,
	} {
		if errors.Is(err, cause) {
			return fmt.Errorf("catalog: %s: %w", operation, cause)
		}
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return fmt.Errorf("catalog: %s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("catalog: %s failed", operation)
}
