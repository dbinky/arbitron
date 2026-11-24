package transfer_test

import (
	"context"
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

func TestTransferClient_ExtraHeadersCloneAndPreserveMultipleValues(t *testing.T) {
	type seen struct {
		path                 string
		team, transfer, late []string
		cf, auth             string
	}
	var mu sync.Mutex
	var got []seen
	hash := strings.Repeat("cd", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		got = append(got, seen{path: r.URL.Path, team: r.Header.Values("X-Team"), transfer: r.Header.Values("X-Arbitron-Transfer"),
			late: r.Header.Values("X-Late"), cf: r.Header.Get("Cf-Access-Client-Id"), auth: r.Header.Get("Authorization")})
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/runners/transfers/manifest":
			_ = json.NewEncoder(w).Encode(runner.NewManifest(1, nil))
		case "/api/v1/runners/transfers/push/commit":
			_ = json.NewEncoder(w).Encode(runner.CheckpointRef{RunID: uuid.New(), Version: 2, ManifestHash: "m"})
		default:
			_, _ = w.Write([]byte("x"))
		}
	}))
	defer srv.Close()

	extra := http.Header{"X-Team": {"blue", "green"}, "Cf-Access-Client-Id": {"cf-id"}, "X-Arbitron-Transfer": {"forged"}}
	c := transfer.NewClient(srv.URL, "ark_k", srv.Client(), extra)
	extra.Set("X-Team", "mutated") // the caller's map changes after construction
	extra.Add("X-Late", "1")

	ctx := context.Background()
	_, err := c.Manifest(ctx, "tok-pull")
	require.NoError(t, err)
	rc, err := c.FetchFile(ctx, "tok-pull", "a.txt")
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.NoError(t, c.PutPart(ctx, "tok-push", 0, []byte("p")))
	_, err = c.CommitPush(ctx, "tok-push", transfer.PushCommit{Parts: 1, SHA256: "s"})
	require.NoError(t, err)
	rc, err = c.FetchBundle(ctx, "tok-b", hash)
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 5, "manifest, file, part, commit and bundle requests")
	wantTokens := map[string]string{
		"/api/v1/runners/transfers/manifest":        "tok-pull",
		"/api/v1/runners/transfers/files":           "tok-pull",
		"/api/v1/runners/transfers/push/0":          "tok-push",
		"/api/v1/runners/transfers/push/commit":     "tok-push",
		"/api/v1/runners/transfers/bundles/" + hash: "tok-b",
	}
	for _, s := range got {
		assert.Equal(t, []string{"blue", "green"}, s.team, "%s: every configured value, in order, from the construction-time clone", s.path)
		assert.Empty(t, s.late, "%s: later caller mutations never leak in", s.path)
		assert.Equal(t, "cf-id", s.cf, s.path)
		assert.Equal(t, "Runner ark_k", s.auth, s.path)
		require.Len(t, s.transfer, 1, "%s: an extra header can never forge the transfer token", s.path)
		assert.NotEqual(t, "forged", s.transfer[0], s.path)
		assert.Equal(t, wantTokens[s.path], s.transfer[0], "%s: the exact scoped token reaches the server", s.path)
	}
}
