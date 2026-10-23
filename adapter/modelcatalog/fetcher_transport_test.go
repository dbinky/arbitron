package modelcatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A supplied I/O leaf must not replace request/redirect validation, timeout,
// size bounds or status handling of the real fetcher.
func TestFetchTransportPreservesGuards(t *testing.T) {
	const source = "https://catalog.example.test/api.json"
	for _, tc := range []struct {
		name, url, redirect, body string
		status                    int
		want                      error
		wantCalls                 int
	}{
		{name: "success", url: source, body: `{}`, status: http.StatusOK, wantCalls: 1},
		{name: "private initial", url: "https://127.0.0.1/catalog", want: outbound.ErrBlockedDialTarget},
		{name: "plaintext initial", url: "http://catalog.example.test/api.json", want: outbound.ErrUnsafeURL},
		{name: "https to http redirect", url: source, status: http.StatusFound, redirect: "http://catalog.example.test/api.json", want: outbound.ErrUnsafeURL, wantCalls: 1},
		{name: "credentials", url: "https://user:secret@catalog.example.test/api.json", want: outbound.ErrUnsafeURL},
		{name: "foreign scheme", url: "file:///tmp/catalog", want: outbound.ErrUnsafeURL},
		{name: "private redirect", url: source, status: http.StatusFound, redirect: "http://169.254.169.254/latest", want: outbound.ErrBlockedDialTarget, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			f := NewFetcherWithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != source || r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" {
					return nil, errors.New("unexpected request")
				}
				deadline, ok := r.Context().Deadline()
				require.True(t, ok)
				require.False(t, deadline.IsZero())
				h := make(http.Header)
				if tc.redirect != "" {
					h.Set("Location", tc.redirect)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: h, Request: r}, nil
			}))
			raw, err := f.Fetch(context.Background(), tc.url)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				assert.Empty(t, raw)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(raw))
			}
			assert.Equal(t, tc.wantCalls, calls)
		})
	}
}
