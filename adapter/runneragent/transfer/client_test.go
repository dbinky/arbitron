package transfer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/runneragent/transfer"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type seenReq struct {
	method, path, query, auth, token, partSHA, extra string
	body                                             []byte
}

func TestClient_SpeaksTheTransferRoutes(t *testing.T) {
	var mu sync.Mutex
	var seen []seenReq
	runID := uuid.New()
	bundleHash := strings.Repeat("ab", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenReq{r.Method, r.URL.Path, r.URL.Query().Get("path"), r.Header.Get("Authorization"), r.Header.Get("X-Arbitron-Transfer"), r.Header.Get("X-Part-SHA256"), r.Header.Get("Cf-Access-Client-Id"), body})
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/runners/transfers/manifest":
			_ = json.NewEncoder(w).Encode(runner.NewManifest(4, []runner.ManifestEntry{{Path: "a", Size: 1, Mode: 0o644, SHA256: "s"}}))
		case "/api/v1/runners/transfers/files", "/api/v1/runners/transfers/bundles/" + bundleHash:
			_, _ = w.Write([]byte("content"))
		case "/api/v1/runners/transfers/push/commit":
			_ = json.NewEncoder(w).Encode(runner.CheckpointRef{RunID: runID, Version: 5, ManifestHash: "new"})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := transfer.NewClient(srv.URL+"/", "ark_secret", srv.Client(), http.Header{"Cf-Access-Client-Id": {"cf-id"}})
	ctx := context.Background()
	m, err := c.Manifest(ctx, "tok-pull")
	require.NoError(t, err)
	assert.Equal(t, int64(4), m.Version)
	require.NoError(t, m.Verify())
	rc, err := c.FetchFile(ctx, "tok-pull", "dir/a b.txt")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	assert.Equal(t, "content", string(b))
	part := []byte("part-zero")
	require.NoError(t, c.PutPart(ctx, "tok-push", 0, part))
	ref, err := c.CommitPush(ctx, "tok-push", transfer.PushCommit{Parts: 1, SHA256: "full"})
	require.NoError(t, err)
	assert.Equal(t, int64(5), ref.Version)
	assert.Equal(t, runID, ref.RunID)
	rc, err = c.FetchBundle(ctx, "tok-b", bundleHash)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	_, err = c.FetchBundle(ctx, "tok-b", "../etc")
	require.Error(t, err, "malformed hashes never reach the server")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 5)
	for _, s := range seen {
		assert.Equal(t, "Runner ark_secret", s.auth, "runner key only in the Authorization header")
		assert.Equal(t, "cf-id", s.extra, "extra (Cloudflare Access) headers on every transfer")
	}
	assert.Equal(t, "GET", seen[0].method)
	assert.Equal(t, "tok-pull", seen[0].token)
	assert.Equal(t, "dir/a b.txt", seen[1].query)
	assert.Equal(t, "PUT", seen[2].method)
	assert.Equal(t, "/api/v1/runners/transfers/push/0", seen[2].path)
	sum := sha256.Sum256(part)
	assert.Equal(t, hex.EncodeToString(sum[:]), seen[2].partSHA)
	assert.Equal(t, part, seen[2].body)
	assert.Equal(t, "POST", seen[3].method)
	assert.JSONEq(t, `{"parts":1,"sha256":"full"}`, string(seen[3].body))
	assert.Equal(t, "tok-b", seen[4].token)
}

func TestClient_NonSuccessIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fenced", http.StatusForbidden) }))
	defer srv.Close()
	c := transfer.NewClient(srv.URL, "ark_k", srv.Client(), nil)
	_, err := c.Manifest(context.Background(), "wrong-job-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.NotContains(t, err.Error(), "ark_k", "the key never appears in errors")
}
