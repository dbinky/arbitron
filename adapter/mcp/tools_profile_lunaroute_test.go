package mcp

import (
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileErrorResult_LunaRouteRefusalVerbatim(t *testing.T) {
	refusal := &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteBackendUnsupported, Model: "glm-5.3", Backend: "codex", Detail: "LunaRoute on codex not yet supported in this release"}
	r := profileErrorResult(fmt.Errorf("prepare secret-connection lr_live_one: %w", refusal))
	require.True(t, r.IsError)
	text := resultText(t, r)
	assert.Equal(t, refusal.Error(), text)
	assert.NotContains(t, text, "lr_live_one")
	assert.NotContains(t, text, "secret-connection")
	spelling := &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteModelSpelling, Model: "latest", Detail: "use a reviewed fixed LunaRoute id"}
	assert.Equal(t, spelling.Error(), resultText(t, profileErrorResult(spelling)))
	// Authz still wins even when a typed refusal is present.
	assert.Equal(t, "forbidden: admin only", resultText(t, profileErrorResult(fmt.Errorf("%w: %w", provider.ErrForbidden, refusal))))
}
