package sources

import (
	"context"
	"github.com/arbitron/arbitron/domain/runner"
)

// EnableClaudeVerification installs the admission verifier and the discovery
// input used to recompute the anthropic_subscription capabilities.
func (i *Inventory) EnableClaudeVerification(v *ClaudeModelVerifier, in DiscoverInput) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.verifier, i.discoverIn = v, in
}

// VerifyModels implements runneragent.ModelVerifier. It never blocks the
// session loop; the returned channel closes when the pass finishes.
func (i *Inventory) VerifyModels(ctx context.Context, models []string) <-chan struct{} {
	done := make(chan struct{})
	i.mu.Lock()
	v, in := i.verifier, i.discoverIn
	i.mu.Unlock()
	if v == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		i.verifyMu.Lock()
		defer i.verifyMu.Unlock()
		v.Request(ctx, models)
		in.ClaudeModels = v.Verified()
		caps, reason := BuildCapabilities(runner.CredAnthropicSubscription, i.HarnessVersions()["claude"], in)
		i.setCapabilities(runner.CredAnthropicSubscription, "", caps, reason)
	}()
	return done
}

func (i *Inventory) setCapabilities(kind runner.CredentialKind, ref string, caps []runner.ExecutionCapability, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for idx := range i.specs {
		if i.specs[idx].Source.Kind == kind && i.specs[idx].Source.CredentialRef == ref {
			i.specs[idx].Source.Capabilities, i.specs[idx].Source.UnavailableReason = cloneCapabilities(caps), reason
			i.last[idx].Capabilities, i.last[idx].UnavailableReason = cloneCapabilities(caps), reason
		}
	}
}
