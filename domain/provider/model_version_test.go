package provider

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Route producers and snapshot/candidate consumers share the modelidentity
// request type; the domain port must not expose credentials or adapter types.
var _ interface {
	VersionRequests(context.Context, Profile) ([]modelidentity.VersionRequest, error)
} = (VersionRequestSource)(nil)

func TestVersionRequestForUsesCoherentProfile(t *testing.T) {
	req := VersionRequestFor(Profile{Alias: "a", Model: "claude-opus-5-5", ConnectionRef: "", Backend: ""}, "")
	assert.Equal(t, VersionRequest{Model: "claude-opus-5-5", Backend: "claude-cli"}, req, "no connection defaults to the subscription backend")

	req = VersionRequestFor(Profile{Alias: "b", Model: "deepseek/deepseek-v4-flash", ConnectionRef: "or", Backend: "opencode"}, "openrouter")
	assert.Equal(t, VersionRequest{Model: "deepseek/deepseek-v4-flash", RouteProvider: "openrouter", ConnectionRef: "or", Backend: "opencode"}, req)
}

func TestVersionRequestForFeedsResolver(t *testing.T) {
	var r VersionResolver = modelidentity.NewVersionResolver()
	got, err := r.SpecificVersion(context.Background(), VersionRequestFor(Profile{Model: "opus-4.8", Backend: "claude-code"}, ""))
	require.NoError(t, err)
	assert.Equal(t, "opus-4.8", got)

	_, err = r.SpecificVersion(context.Background(), VersionRequestFor(Profile{Model: "sonnet"}, ""))
	assert.ErrorIs(t, err, ErrSpecificVersionUnavailable)
}
