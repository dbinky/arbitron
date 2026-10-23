package modelidentity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityPolicySeedsD2Table(t *testing.T) {
	rules := modelidentity.InitialRules()
	type key struct{ provider, tier string }
	got := map[key]string{}
	rows := 0
	for _, r := range rules {
		groups := []int{1}
		if len(r.Providers) == 1 && r.Providers[0] == "anthropic" {
			groups = []int{1, 2}
		}
		assert.Equal(t, groups, r.VersionGroups, r.Tier)
		for _, p := range r.Providers {
			got[key{p, r.Tier}] = r.Pattern
			rows++
		}
	}
	want := map[key]string{
		{"anthropic", "opus"}:         `claude-opus-(\d{1,3})(?:-(\d{1,3}))?`,
		{"anthropic", "sonnet"}:       `claude-sonnet-(\d{1,3})(?:-(\d{1,3}))?`,
		{"anthropic", "haiku"}:        `claude-haiku-(\d{1,3})(?:-(\d{1,3}))?`,
		{"anthropic", "fable"}:        `claude-fable-(\d{1,3})(?:-(\d{1,3}))?`,
		{"openai", "sol"}:             `gpt-(\d+(?:\.\d+)?)-sol`,
		{"openai", "astra"}:           `gpt-(\d+(?:\.\d+)?)-astra`,
		{"openai", "luna"}:            `gpt-(\d+(?:\.\d+)?)-luna`,
		{"openai", "terra"}:           `gpt-(\d+(?:\.\d+)?)-terra`,
		{"deepseek", "flash"}:         `deepseek-v(\d+(?:\.\d+)?)-flash`,
		{"ollama-cloud", "flash"}:     `deepseek-v(\d+(?:\.\d+)?)-flash`,
		{"openrouter", "flash"}:       `deepseek-v(\d+(?:\.\d+)?)-flash`,
		{"deepseek", "pro"}:           `deepseek-v(\d+(?:\.\d+)?)-pro`,
		{"ollama-cloud", "pro"}:       `deepseek-v(\d+(?:\.\d+)?)-pro`,
		{"openrouter", "pro"}:         `deepseek-v(\d+(?:\.\d+)?)-pro`,
		{"zhipuai", "glm"}:            `glm-(\d+(?:\.\d+)?)`,
		{"ollama-cloud", "glm"}:       `glm-(\d+(?:\.\d+)?)`,
		{"zhipuai", "glm-flash"}:      `glm-(\d+(?:\.\d+)?)-flash`,
		{"ollama-cloud", "glm-flash"}: `glm-(\d+(?:\.\d+)?)-flash`,
		{"moonshotai", "kimi"}:        `kimi-k(\d+(?:\.\d+)?)`,
		{"ollama-cloud", "kimi"}:      `kimi-k(\d+(?:\.\d+)?)`,
		{"moonshotai", "kimi-code"}:   `kimi-k(\d+(?:\.\d+)?)-code`,
		{"ollama-cloud", "kimi-code"}: `kimi-k(\d+(?:\.\d+)?)-code`,
	}
	assert.Equal(t, want, got)
	assert.Equal(t, len(want), rows, "no duplicate or extra provider/tier rows")
	require.NoError(t, modelidentity.ValidateRules(rules), "the shipped table must pass its own validation")
}

func TestIdentityPolicySeedsD2WorkedExamples(t *testing.T) {
	matcher, err := modelidentity.CompileRules(modelidentity.InitialRules())
	require.NoError(t, err)
	tests := []struct {
		provider, id string
		tier         string
		match        bool
		version      []int
	}{
		{"anthropic", "claude-opus-5-5", "opus", true, nil},
		{"anthropic", "claude-opus-5", "opus", true, nil},
		{"anthropic", "claude-opus-12-34", "opus", true, nil},
		{"anthropic", "claude-opus-123-456", "opus", true, nil},
		{"anthropic", "claude-opus-4-20250514", "", false, nil},
		{"anthropic", "claude-opus-4-0731", "", false, nil},
		{"anthropic", "claude-opus-20250514", "", false, nil},
		{"anthropic", "claude-haiku-4-5-20251001", "", false, nil},
		{"anthropic", "claude-opus-5-5-fast", "", false, nil},
		{"openai", "gpt-6.1-sol", "sol", true, nil},
		{"ollama-cloud", "deepseek-v4-flash:0731", "", false, nil},
		{"ollama-cloud", "deepseek-v4.1-flash", "flash", true, nil},
		{"ollama-cloud", "kimi-k3", "kimi", true, nil},
		{"ollama-cloud", "kimi-k2.7-code", "kimi-code", true, nil},
		{"anthropic", "gpt-6.1-sol", "", false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.provider+"/"+tc.id, func(t *testing.T) {
			hits := matcher.Match(tc.provider, tc.id)
			if !tc.match {
				assert.Empty(t, hits)
				return
			}
			require.Len(t, hits, 1)
			assert.Equal(t, tc.tier, hits[0].Tier)
		})
	}
	hits := matcher.Match("anthropic", "claude-opus-5-5")
	require.Len(t, hits, 1)
	assert.Equal(t, []string{"5", "5"}, hits[0].Captures, "version groups select the numeric captures in order")
}

func TestIdentityPolicyDottedAndHyphenVersions(t *testing.T) {
	src := &staticPolicy{policy: modelidentity.IdentityPolicy{Revision: 7, Rules: []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)(?:[.-](\d+))?`, VersionGroups: []int{1, 2}}}}}
	mapper := newMapper(t, src)
	dotted := mapOne(t, mapper, "anthropic", "claude-opus-5.10", "2026-10-02")
	hyphen := mapOne(t, mapper, "anthropic", "claude-opus-5-10", "2026-10-01")
	assert.Equal(t, "5.10", dotted.OrderKey)
	assert.Equal(t, dotted.OrderKey, hyphen.OrderKey)
	assert.Equal(t, int64(7), dotted.PolicyRevision)
	cmp, err := modelidentity.Compare(dotted, hyphen)
	require.NoError(t, err)
	assert.Equal(t, 1, cmp, "equal numeric tuples use the real release date")
	older := mapOne(t, mapper, "anthropic", "claude-opus-5-9", "2026-10-03")
	cmp, err = modelidentity.Compare(dotted, older)
	require.NoError(t, err)
	assert.Equal(t, 1, cmp, "numeric order wins over the release date")
	for _, id := range []string{"claude-opus-5.10-preview", "prefix-claude-opus-5-10", "foreign/claude-opus-5.10"} {
		entry := mapOne(t, mapper, "anthropic", id, "")
		assert.Empty(t, entry.Tier, id)
		assert.Empty(t, entry.OrderKey, id)
	}
}

func TestValidateRulesRejects(t *testing.T) {
	ok := modelidentity.PatternRule{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}
	with := func(f func(*modelidentity.PatternRule)) []modelidentity.PatternRule {
		r := ok
		r.Providers = append([]string(nil), ok.Providers...)
		r.VersionGroups = append([]int(nil), ok.VersionGroups...)
		f(&r)
		return []modelidentity.PatternRule{r}
	}
	cases := map[string][]modelidentity.PatternRule{
		"empty provider list":  with(func(r *modelidentity.PatternRule) { r.Providers = nil }),
		"blank provider":       with(func(r *modelidentity.PatternRule) { r.Providers = []string{" "} }),
		"empty tier":           with(func(r *modelidentity.PatternRule) { r.Tier = " " }),
		"empty pattern":        with(func(r *modelidentity.PatternRule) { r.Pattern = "" }),
		"malformed regex":      with(func(r *modelidentity.PatternRule) { r.Pattern = `claude-(` }),
		"backreference":        with(func(r *modelidentity.PatternRule) { r.Pattern = `claude-(\d+)\1` }),
		"no version groups":    with(func(r *modelidentity.PatternRule) { r.VersionGroups = nil }),
		"capture out of range": with(func(r *modelidentity.PatternRule) { r.VersionGroups = []int{2} }),
		"capture zero":         with(func(r *modelidentity.PatternRule) { r.VersionGroups = []int{0} }),
		"duplicate capture":    with(func(r *modelidentity.PatternRule) { r.VersionGroups = []int{1, 1} }),
		"nonnumeric capture":   with(func(r *modelidentity.PatternRule) { r.Pattern = `claude-opus-(\w+)` }),
		"wildcard capture":     with(func(r *modelidentity.PatternRule) { r.Pattern = `claude-opus-(.+)` }),
		"too many rules":       manyRules(modelidentity.MaxRules + 1),
		"oversized pattern": with(func(r *modelidentity.PatternRule) {
			r.Pattern = `claude-opus-(\d+)` + longLiteral(modelidentity.MaxPatternLen)
		}),
		"contradictory targets": {ok, {Providers: []string{"anthropic"}, Tier: "sonnet", Pattern: ok.Pattern, VersionGroups: []int{1}}},
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			err := modelidentity.ValidateRules(rules)
			require.Error(t, err)
			assert.True(t, errors.Is(err, modelidentity.ErrInvalid), "got %v", err)
		})
	}
	t.Run("empty policy is invalid", func(t *testing.T) {
		assert.ErrorIs(t, modelidentity.ValidateRules(nil), modelidentity.ErrInvalid)
	})
	t.Run("same pattern on different providers with different tiers is allowed", func(t *testing.T) {
		other := ok
		other.Providers = []string{"openrouter"}
		other.Tier = "sonnet"
		assert.NoError(t, modelidentity.ValidateRules([]modelidentity.PatternRule{ok, other}))
	})
}

func manyRules(n int) []modelidentity.PatternRule {
	rules := make([]modelidentity.PatternRule, n)
	for i := range rules {
		rules[i] = modelidentity.PatternRule{Providers: []string{"p"}, Tier: "t", Pattern: `m-(\d+)-` + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)), VersionGroups: []int{1}}
	}
	return rules
}

func longLiteral(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestMatchAmbiguousIDReturnsEveryHit(t *testing.T) {
	rules := []modelidentity.PatternRule{
		{Providers: []string{"p"}, Tier: "a", Pattern: `m-(\d+)`, VersionGroups: []int{1}},
		{Providers: []string{"p"}, Tier: "b", Pattern: `m-(\d+)`, VersionGroups: []int{1}},
	}
	// Identical provider/pattern with different targets is rejected on write...
	require.ErrorIs(t, modelidentity.ValidateRules(rules), modelidentity.ErrInvalid)
	// ...and overlapping-but-different patterns are reported to the mapper as multiple hits, never "first wins".
	overlap := []modelidentity.PatternRule{
		{Providers: []string{"p"}, Tier: "a", Pattern: `m-(\d+)`, VersionGroups: []int{1}},
		{Providers: []string{"p"}, Tier: "b", Pattern: `m-([0-9]+)`, VersionGroups: []int{1}},
	}
	m, err := modelidentity.CompileRules(overlap)
	require.NoError(t, err)
	assert.Len(t, m.Match("p", "m-5"), 2)
}

type fakeStore struct {
	policy    modelidentity.IdentityPolicy
	replaced  int
	snapErr   error
	replErr   error
	lastRules []modelidentity.PatternRule
}

func (f *fakeStore) Snapshot(context.Context) (modelidentity.IdentityPolicy, error) {
	return f.policy, f.snapErr
}

func (f *fakeStore) Replace(_ context.Context, expected int64, rules []modelidentity.PatternRule) (modelidentity.IdentityPolicy, error) {
	f.replaced++
	if f.replErr != nil {
		return modelidentity.IdentityPolicy{}, f.replErr
	}
	if expected != f.policy.Revision {
		return modelidentity.IdentityPolicy{}, modelidentity.ErrConflict
	}
	f.lastRules = rules
	f.policy = modelidentity.IdentityPolicy{Revision: expected + 1, Rules: rules}
	return f.policy, nil
}

type fakeGate struct{ calls int }

func (g *fakeGate) WithMutation(ctx context.Context, fn func(context.Context) error) error {
	g.calls++
	return fn(ctx)
}

func adminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "a", Role: identity.RoleAdmin})
}

func userCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser})
}

func newService(t *testing.T) (*modelidentity.PolicyService, *fakeStore, *fakeGate) {
	t.Helper()
	store := &fakeStore{policy: modelidentity.IdentityPolicy{Revision: 1, Rules: modelidentity.InitialRules()}}
	gate := &fakeGate{}
	svc := modelidentity.NewPolicyService(store)
	svc.SetMutationGate(gate)
	return svc, store, gate
}

func TestPolicyServiceAuthorizationAndGate(t *testing.T) {
	svc, store, gate := newService(t)
	newRules := []modelidentity.PatternRule{{Providers: []string{"Anthropic "}, Tier: " Opus", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}}

	for _, ctx := range []context.Context{context.Background(), userCtx()} {
		_, err := svc.Get(ctx)
		require.Error(t, err)
		_, err = svc.Update(ctx, 1, newRules)
		require.Error(t, err)
	}
	assert.Zero(t, store.replaced, "unauthorized updates never reach the store")
	assert.Zero(t, gate.calls)

	got, err := svc.Update(adminCtx(), 1, newRules)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Revision)
	assert.Equal(t, []string{"anthropic"}, got.Rules[0].Providers, "provider and tier are normalized; the model spelling is preserved")
	assert.Equal(t, "opus", got.Rules[0].Tier)
	assert.Equal(t, `claude-opus-(\d+)`, got.Rules[0].Pattern)
	assert.Equal(t, 1, gate.calls, "the write runs inside the mutation gate")

	_, err = svc.Update(adminCtx(), 1, newRules)
	assert.ErrorIs(t, err, modelidentity.ErrConflict, "a stale expected revision conflicts")
}

func TestPolicyServiceNilGateRejectsUpdates(t *testing.T) {
	store := &fakeStore{policy: modelidentity.IdentityPolicy{Revision: 1, Rules: modelidentity.InitialRules()}}
	svc := modelidentity.NewPolicyService(store)
	_, err := svc.Update(adminCtx(), 1, modelidentity.InitialRules())
	require.Error(t, err)
	assert.Zero(t, store.replaced, "a missing gate is an error, never a successful no-op")
	_, err = svc.Get(adminCtx())
	assert.NoError(t, err, "reads do not need the gate")
}

func TestPolicyServiceInvalidAndFaultsKeepPolicy(t *testing.T) {
	svc, store, _ := newService(t)
	_, err := svc.Update(adminCtx(), 1, []modelidentity.PatternRule{{Providers: []string{"p"}, Tier: "t", Pattern: `(`, VersionGroups: []int{1}}})
	require.ErrorIs(t, err, modelidentity.ErrInvalid)
	assert.Zero(t, store.replaced)

	store.replErr = errors.New("disk on fire")
	_, err = svc.Update(adminCtx(), 1, modelidentity.InitialRules())
	require.Error(t, err)
	assert.Equal(t, int64(1), store.policy.Revision)

	store.snapErr = errors.New("read failed")
	_, err = svc.ReadInternal(context.Background())
	require.Error(t, err, "an unreadable policy is an error, never an empty policy that authorizes nothing silently")
}

func TestPolicyServiceDeepCopies(t *testing.T) {
	svc, store, _ := newService(t)
	got, err := svc.Get(adminCtx())
	require.NoError(t, err)
	got.Rules[0].Providers[0] = "mutated"
	got.Rules[0].VersionGroups[0] = 99
	again, err := svc.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "anthropic", again.Rules[0].Providers[0])
	assert.Equal(t, store.policy.Rules[0].VersionGroups, again.Rules[0].VersionGroups)
	assert.NotEqual(t, 99, again.Rules[0].VersionGroups[0])

	// The caller's input is copied too.
	in := []modelidentity.PatternRule{{Providers: []string{"p"}, Tier: "t", Pattern: `m-(\d+)`, VersionGroups: []int{1}}}
	_, err = svc.Update(adminCtx(), 1, in)
	require.NoError(t, err)
	in[0].Providers[0] = "mutated"
	assert.Equal(t, "p", store.lastRules[0].Providers[0])
}

func TestPolicyServiceSnapshotImplementsPolicySource(t *testing.T) {
	svc, _, _ := newService(t)
	var src modelidentity.PolicySource = svc
	p, err := src.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), p.Revision)
	assert.NotEmpty(t, p.Rules)
}
