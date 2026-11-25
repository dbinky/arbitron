package runner_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
)

type staticModels struct {
	models []string
	err    error
}

func (s staticModels) RequiredClaudeModels(context.Context) ([]string, error) { return s.models, s.err }

func TestHubAdmit_SendsRequiredClaudeModelsSortedAndDeduplicated(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	h.SetRequiredModels(staticModels{models: []string{"claude-sonnet-4-5", "", "claude-opus-5-5", "claude-sonnet-4-5"}})
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	first := conn.Sent()[0]
	require.Equal(t, runner.MsgAdmitted, first.Kind)
	require.NotNil(t, first.Admitted)
	assert.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-4-5"}, first.Admitted.ClaudeModels)
}

func TestHubAdmit_RequiredModelsAreCappedAt32(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf("claude-m-%02d", i))
	}
	h.SetRequiredModels(staticModels{models: many})
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	assert.Len(t, conn.Sent()[0].Admitted.ClaudeModels, 32)
}

func TestHubAdmit_ModelSourceErrorStillAdmitsWithNoModels(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	h.SetRequiredModels(staticModels{err: errors.New("profiles unavailable")})
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.Equal(t, runner.MsgAdmitted, conn.Sent()[0].Kind)
	assert.Empty(t, conn.Sent()[0].Admitted.ClaudeModels)
	assert.Contains(t, p.EventKinds(t), "runner.models_unavailable")
}

func TestHubAdmit_NoModelSourceSendsAnEmptyList(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NotNil(t, conn.Sent()[0].Admitted)
	assert.Empty(t, conn.Sent()[0].Admitted.ClaudeModels)
}
