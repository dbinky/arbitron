package modelidentity_test

import (
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityPolicyProviderLimitBeforeNormalization(t *testing.T) {
	for _, count := range []int{modelidentity.MaxProvidersPerRule, modelidentity.MaxProvidersPerRule + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			providers := make([]string, count)
			for i := range providers {
				providers[i] = " Anthropic "
			}
			expectedProviders := append([]string(nil), providers...)
			rules := []modelidentity.PatternRule{{Providers: providers, Tier: " Opus ", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}}
			validationErr := modelidentity.ValidateRules(rules)
			matcher, compileErr := modelidentity.CompileRules(rules)
			svc, store, gate := newService(t)
			before := store.policy
			updated, updateErr := svc.Update(adminCtx(), before.Revision, rules)
			assert.Equal(t, expectedProviders, rules[0].Providers, "validation must not mutate the caller's input")
			if count > modelidentity.MaxProvidersPerRule {
				assert.ErrorIs(t, validationErr, modelidentity.ErrInvalid)
				assert.ErrorIs(t, compileErr, modelidentity.ErrInvalid)
				assert.Nil(t, matcher)
				assert.ErrorIs(t, updateErr, modelidentity.ErrInvalid)
				assert.Empty(t, updated)
				assert.Zero(t, store.replaced, "an oversized submission must not reach persistence")
				assert.Zero(t, gate.calls, "reject before entering the mutation lease")
				assert.Equal(t, before, store.policy)
				return
			}
			require.NoError(t, validationErr)
			require.NoError(t, compileErr)
			assert.Equal(t, []modelidentity.Hit{{Tier: "opus", Captures: []string{"5"}}}, matcher.Match("anthropic", "claude-opus-5"))
			require.NoError(t, updateErr)
			assert.Equal(t, int64(2), updated.Revision)
			assert.Equal(t, []string{"anthropic"}, updated.Rules[0].Providers)
			assert.Equal(t, rules[0].Pattern, updated.Rules[0].Pattern)
		})
	}
}
