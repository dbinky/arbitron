package modelcatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only tests in this package can substitute a transport. Production callers use
// NewFetcher, which always constructs the bounded, guarded outbound client.
func fixtureFetcher(server *httptest.Server) *Fetcher {
	f := NewFetcher()
	f.client, f.allowHTTP = server.Client(), true
	return f
}

func TestFetchExactConfiguredURLAndBody(t *testing.T) {
	const body = `{"anthropic":{"id":"anthropic","models":{}},"openai":{"id":"openai","models":{}}}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/configured/catalog?revision=7", r.URL.RequestURI())
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	got, err := fixtureFetcher(server).Fetch(context.Background(), server.URL+"/configured/catalog?revision=7")
	require.NoError(t, err)
	assert.Equal(t, body, string(got), "raw producer must preserve every provider byte, not filter followers")
	assert.Equal(t, 1, calls)
}

func TestFetchBoundsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		length bool
	}{
		{"empty", http.StatusOK, "", false},
		{"non-success", http.StatusServiceUnavailable, "down", false},
		{"oversized-known", http.StatusOK, strings.Repeat("x", maxCatalogBytes+1), true},
		{"oversized-chunked", http.StatusOK, strings.Repeat("x", maxCatalogBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.length {
					w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
				}
				w.WriteHeader(tc.status)
				if !tc.length {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			got, err := fixtureFetcher(server).Fetch(context.Background(), server.URL)
			require.Error(t, err)
			assert.Nil(t, got, "partial/failed download must never reach catalog replacement")
		})
	}
}

func TestFetchExactSizeLimit(t *testing.T) {
	body := strings.Repeat("x", maxCatalogBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	got, err := fixtureFetcher(server).Fetch(context.Background(), server.URL)
	require.NoError(t, err)
	require.Len(t, got, len(body))
	assert.True(t, string(got) == body, "full body at the exact limit must be preserved")
}

func TestFetchTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	f := fixtureFetcher(server)
	f.client.Timeout = 20 * time.Millisecond
	_, err := f.Fetch(context.Background(), server.URL+"?token=SOURCESECRET")
	require.Error(t, err)
	assertFetchErrorRedacted(t, err)
	var timeout interface{ Timeout() bool }
	require.ErrorAs(t, err, &timeout)
	assert.True(t, timeout.Timeout())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.Fetch(ctx, server.URL+"?token=SOURCESECRET")
	assert.ErrorIs(t, err, context.Canceled)
	assertFetchErrorRedacted(t, err)
}

func TestFetchReadFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "truncated")
	}))
	defer server.Close()
	got, err := fixtureFetcher(server).Fetch(context.Background(), server.URL+"?token=SOURCESECRET")
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assertFetchErrorRedacted(t, err)
	assert.Nil(t, got)
}

func TestFetchProductionGuard(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, "must not be fetched")
	}))
	defer server.Close()
	f := NewFetcher()
	assert.Equal(t, 30*time.Second, f.client.Timeout)
	for _, raw := range []string{server.URL, "file:///etc/passwd", "https://user:password@example.com/catalog"} {
		got, err := f.Fetch(context.Background(), raw)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.True(t, errors.Is(err, outbound.ErrUnsafeURL) || errors.Is(err, outbound.ErrBlockedDialTarget), "%v", err)
	}
	assert.Zero(t, calls)
}
