package procenv

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type redirectTransport struct{ calls int }

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/steal"}},
		Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

func TestNoRedirectClient(t *testing.T) {
	tr := &redirectTransport{}
	base := &http.Client{Transport: tr, Timeout: 7 * time.Second}
	c := NoRedirectClient(base)
	req, err := http.NewRequest(http.MethodGet, "https://gw.lunaroute.com/v1/models", nil)
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, 1, tr.calls, "the credential-bearing request is not replayed")
	assert.Equal(t, 7*time.Second, c.Timeout)
	assert.Same(t, tr, c.Transport)
	assert.Nil(t, base.CheckRedirect, "the shared client is not mutated")

	fresh := NoRedirectClient(nil)
	require.NotNil(t, fresh)
	require.NotNil(t, fresh.CheckRedirect)
	assert.ErrorIs(t, fresh.CheckRedirect(req, nil), http.ErrUseLastResponse)
}
