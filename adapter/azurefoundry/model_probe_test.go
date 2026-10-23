package azurefoundry_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure has no exact-execution producer (B5): a deployment name is not proof of
// the model behind it, so a request that requires the executed model, or a
// strict pinned binding (without a D8 native exception), must refuse before
// any request leaves the process.
func TestExactModelRequiredRefusesBeforeSending(t *testing.T) {
	for name, tc := range map[string]struct {
		pinned  bool
		require bool
	}{
		"pinned binding":      {pinned: true},
		"require executed":    {require: true},
		"pinned and required": {pinned: true, require: true},
	} {
		t.Run(name, func(t *testing.T) {
			var cap captured
			sent := 0
			client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
				sent++
				return jsonResp(200, okChatBody), nil
			})
			b := resourceBinding()
			b.Pinned = tc.pinned
			p := azurefoundry.New(azurefoundry.Config{Binding: b, HTTPClient: client})
			req := engine.ChatRequest{Model: b.Model, Prompt: "hi", RequireExecutedModel: tc.require}

			resp, err := p.Chat(context.Background(), req)
			require.ErrorIs(t, err, azurefoundry.ErrModelExecutionEvidenceUnavailable)
			assert.Nil(t, resp)

			ch, err := p.StreamChat(context.Background(), req)
			require.ErrorIs(t, err, azurefoundry.ErrModelExecutionEvidenceUnavailable)
			assert.Nil(t, ch)
			assert.Zero(t, sent, "an unverifiable execution must not reach the upstream")
		})
	}
}

// Edge: an ordinary unpinned request is unaffected.
func TestUnpinnedRequestStillSends(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) { return jsonResp(200, okChatBody), nil })
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Model: "gpt-4o-deploy", Prompt: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "hello there", resp.Output)
}
