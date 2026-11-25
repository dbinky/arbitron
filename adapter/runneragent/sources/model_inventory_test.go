package sources

import (
	"context"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestInventory_VerifyModelsRecomputesTheClaudeSourceForTheNextHeartbeat(t *testing.T) {
	c := &scriptedConfirm{out: map[string]runner.ConfirmationResult{"claude-opus-5-5": okResult("claude-opus-5-5")}}
	in := DiscoverInput{Rules: claudeTestRules(t)}
	inv := New([]Spec{{Source: claudeSubscriptionSource(), Prober: nopProber{}}}, map[string]string{"claude": "2.1.400 (Claude Code)"}, nowFixed)
	inv.EnableClaudeVerification(NewClaudeModelVerifier(c, "/scratch", nil), in)
	before := inv.Sources(context.Background())
	require.Empty(t, before[0].Capabilities)

	done := inv.VerifyModels(context.Background(), []string{"claude-opus-5-5"})
	<-done
	after := inv.Sources(context.Background())
	require.Len(t, after[0].Capabilities, 2, "chat and stream")
	assert.Equal(t, []string{"claude-opus-5-5"}, after[0].Capabilities[0].Models)
	assert.Empty(t, after[0].UnavailableReason)
	probed := inv.Probe(context.Background())
	assert.Len(t, probed[0].Capabilities, 2, "a probe keeps the verified capabilities")
}
func claudeSubscriptionSource() runneragent.Source {
	return runneragent.Source{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{""}}
}

type nopProber struct{}

func (nopProber) Probe(context.Context, runner.Source) (runner.ProbeResult, error) {
	return runner.ProbeResult{Auth: "ok"}, nil
}
func nowFixed() time.Time { return time.Unix(0, 0).UTC() }
