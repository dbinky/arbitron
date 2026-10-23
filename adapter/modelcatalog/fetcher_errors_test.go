package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Wrapping raw request/transport errors, or sanitizing only url.Error.URL,
// exposes query tokens in diagnostics (including nested redirect/read errors).
func assertFetchErrorRedacted(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		assert.NotContains(t, fmt.Sprintf("%+v", cause), "SOURCESECRET")
		assert.NotContains(t, fmt.Sprintf("%+v", cause), "TARGETSECRET")
		assert.NotContains(t, fmt.Sprintf("%+v", cause), "PASSWORDSECRET")
	}
}

func TestFetchFailuresDoNotExposeURLs(t *testing.T) {
	for _, raw := range []string{
		"https://catalog.example/%zz?token=SOURCESECRET",
		"https://localhost/catalog?token=SOURCESECRET",
		"https://user:PASSWORDSECRET@catalog.example/catalog?token=SOURCESECRET",
		"file:///catalog?token=SOURCESECRET",
	} {
		t.Run(raw, func(t *testing.T) {
			body, err := NewFetcher().Fetch(context.Background(), raw)
			assert.Nil(t, body)
			assertFetchErrorRedacted(t, err)
		})
	}
}

func TestFetchRedirectFailuresDoNotExposeURLs(t *testing.T) {
	for _, target := range []string{
		"https://127.0.0.1/forbidden?token=TARGETSECRET",
		"https://user:PASSWORDSECRET@catalog.example/forbidden?token=TARGETSECRET",
		"https://catalog.example/%zz?token=TARGETSECRET",
		"https://catalog.example/loop?token=TARGETSECRET",
		// An https source must not be downgraded to plaintext mid-chain.
		"http://catalog.example/downgrade?token=TARGETSECRET",
	} {
		t.Run(target, func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusFound)
			}))
			defer server.Close()
			f := fixtureFetcher(server)
			base := f.client.Transport
			f.client.Transport = fetchRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.Hostname() == "catalog.example" {
					req = req.Clone(req.Context())
					u := *req.URL
					u.Host, u.Scheme = server.Listener.Addr().String(), "http"
					req.URL = &u
				}
				return base.RoundTrip(req)
			})
			// Keep the production redirect policy; only the initial loopback
			// transport is substituted so the HTTP response is deterministic.
			f.client.CheckRedirect = NewFetcher().client.CheckRedirect
			body, err := f.Fetch(context.Background(), server.URL+"/catalog?token=SOURCESECRET")
			assert.Nil(t, body)
			assertFetchErrorRedacted(t, err)
			if strings.Contains(target, "/loop") {
				assert.Equal(t, []string{"/catalog", "/loop", "/loop", "/loop"}, paths)
			} else {
				assert.Equal(t, []string{"/catalog"}, paths, "unsafe redirects must never be fetched")
			}
		})
	}
}

type fetchRoundTripper func(*http.Request) (*http.Response, error)

func (f fetchRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchTransportAndReadErrorsDoNotExposeURLs(t *testing.T) {
	for _, readBody := range []bool{false, true} {
		t.Run(fmt.Sprint(readBody), func(t *testing.T) {
			f := NewFetcher()
			f.client.Transport = fetchRoundTripper(func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "/catalog?token=SOURCESECRET", req.URL.RequestURI())
				err := fmt.Errorf("failed request %s target=TARGETSECRET", req.URL)
				if !readBody {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(iotest.ErrReader(err)), ContentLength: -1}, nil
			})
			body, err := f.Fetch(context.Background(), "https://catalog.example/catalog?token=SOURCESECRET")
			assert.Nil(t, body)
			assertFetchErrorRedacted(t, err)
		})
	}
}

func TestFetchRedactionPreservesFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want error
	}{
		{"https://localhost/catalog?token=SOURCESECRET", outbound.ErrBlockedDialTarget},
		{"https://user:PASSWORDSECRET@catalog.example/catalog?token=SOURCESECRET", outbound.ErrUnsafeURL},
	} {
		_, err := NewFetcher().Fetch(context.Background(), tc.url)
		assert.ErrorIs(t, err, tc.want)
		assertFetchErrorRedacted(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFetcher().Fetch(ctx, "https://catalog.example/catalog?token=SOURCESECRET")
	assert.ErrorIs(t, err, context.Canceled)
	assertFetchErrorRedacted(t, err)
}
