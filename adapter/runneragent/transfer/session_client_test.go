package transfer_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/engine"
)

func TestClient_SessionBundlePutAndGet(t *testing.T) {
	data := []byte("bundle-bytes")
	var mu sync.Mutex
	var route, token, sum string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		route, token = r.Method+" "+r.URL.Path, r.Header.Get("X-Arbitron-Transfer")
		if r.Method == http.MethodPut {
			sum = r.Header.Get(runnerproto.SessionChecksumHeader)
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	c := transfer.NewClient(srv.URL, "ark_k", srv.Client(), nil)
	ctx := context.Background()

	require.NoError(t, c.PutSession(ctx, "rtt_s", "job-1", data))
	mu.Lock()
	assert.True(t, strings.HasPrefix(route, "PUT ") && strings.HasSuffix(route, "/transfers/sessions/job-1"), route)
	assert.Equal(t, "rtt_s", token)
	assert.Equal(t, engine.BundleChecksum(data), sum)
	assert.Equal(t, data, body)
	mu.Unlock()

	got, err := c.GetSession(ctx, "rtt_s", "job-1", int64(len(data)))
	require.NoError(t, err)
	assert.Equal(t, data, got)
	_, err = c.GetSession(ctx, "rtt_s", "job-1", int64(len(data))-1)
	assert.Error(t, err, "more bytes than announced are refused")
	_, err = c.GetSession(ctx, "rtt_s", "job-1", int64(len(data))+1)
	assert.Error(t, err, "fewer bytes than announced are refused")
}
