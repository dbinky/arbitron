package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Edge-case: when {base}/models 404s, the probe falls back to Ollama's native
// {base}/api/tags, trimming a trailing /v1 from the base first.
func TestOpenAICompatProbe_404_FallsBackToOllamaNative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusNotFound)
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3:8b"}]}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "ollama",
		BaseURL:  srv.URL + "/v1",
	})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.Equal(t, []string{"llama3:8b"}, res.Models)
}

// Error-handling: a non-2xx from the native /api/tags fallback is still classified.
func TestOpenAICompatProbe_OllamaNative_401_CredentialsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.WriteHeader(http.StatusNotFound)
		case "/api/tags":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{Provider: "ollama", BaseURL: srv.URL})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialsRejected)
}
