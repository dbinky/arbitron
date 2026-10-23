package modelidentity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticPolicy struct {
	policy modelidentity.IdentityPolicy
	err    error
	reads  int
}

func (s *staticPolicy) Snapshot(context.Context) (modelidentity.IdentityPolicy, error) {
	s.reads++
	if s.err != nil {
		return modelidentity.IdentityPolicy{}, s.err
	}
	return s.policy, nil
}

func initialPolicy() *staticPolicy {
	return &staticPolicy{policy: modelidentity.IdentityPolicy{Revision: 1, Rules: modelidentity.InitialRules()}}
}

func newMapper(t *testing.T, src modelidentity.PolicySource) *modelidentity.IdentityMapper {
	t.Helper()
	m, err := modelidentity.NewIdentityMapper(src)
	require.NoError(t, err)
	return m
}

func mapOne(t *testing.T, m *modelidentity.IdentityMapper, provider, native, date string) modelidentity.Entry {
	t.Helper()
	e, err := m.Map(context.Background(), modelidentity.CatalogModel{CatalogProvider: provider, NativeID: native, ReleaseDate: date})
	require.NoError(t, err)
	return e
}

func TestNewIdentityMapperRejectsNilSource(t *testing.T) {
	_, err := modelidentity.NewIdentityMapper(nil)
	require.Error(t, err)
	var typedNil *staticPolicy
	_, err = modelidentity.NewIdentityMapper(typedNil)
	require.Error(t, err)
}

// Happy: evidenced chronology and one canonical identity across two native paths.
func TestIdentitySameTargetNewer(t *testing.T) {
	m := newMapper(t, initialPolicy())

	older := mapOne(t, m, "ollama-cloud", "deepseek-v4-flash", "2026-05-01")
	newer := mapOne(t, m, "ollama-cloud", "deepseek-v4.1-flash", "2026-08-01")
	assert.Equal(t, "flash", newer.Tier)
	assert.Equal(t, "ollama-cloud", newer.Provider)
	cmp, err := modelidentity.Compare(newer, older)
	require.NoError(t, err)
	assert.Equal(t, 1, cmp)
	cmp, err = modelidentity.Compare(older, newer)
	require.NoError(t, err)
	assert.Equal(t, -1, cmp)

	// One DeepSeek model on three catalog providers is one canonical identity,
	// and each entry keeps its own catalog provider as the follow target.
	a := mapOne(t, m, "deepseek", "deepseek-v4-flash", "2026-05-01")
	b := mapOne(t, m, "ollama-cloud", "deepseek-v4-flash", "2026-05-01")
	c := mapOne(t, m, "openrouter", "deepseek/deepseek-v4-flash", "2026-05-01")
	assert.Equal(t, a.CanonicalID, b.CanonicalID)
	assert.Equal(t, a.CanonicalID, c.CanonicalID)
	assert.Equal(t, "deepseek/deepseek-v4-flash", a.CanonicalID)
	assert.Equal(t, []string{"deepseek", "ollama-cloud", "openrouter"}, []string{a.Provider, b.Provider, c.Provider})
	assert.Equal(t, "deepseek/deepseek-v4-flash", c.NativeID, "native spelling is preserved for the adapter")
	assert.Equal(t, "flash", c.Tier)
	assert.Equal(t, int64(1), a.PolicyRevision)
}

// Success: a specific model keeps the spelling each adapter needs.
func TestIdentityPreservesExecutableSpelling(t *testing.T) {
	m := newMapper(t, initialPolicy())
	e := mapOne(t, m, "openrouter", "deepseek/deepseek-v4-flash", "")
	assert.Equal(t, "deepseek/deepseek-v4-flash", e.SpecificModel)
	assert.True(t, e.Immutable)

	e = mapOne(t, m, "anthropic", "claude-opus-5-5", "")
	assert.Equal(t, "claude-opus-5-5", e.SpecificModel)
	assert.Equal(t, "opus", e.Tier)
	assert.Equal(t, "5.5", e.OrderKey)
}

// Edge: numeric order, not string order.
func TestIdentityNumericOrder(t *testing.T) {
	m := newMapper(t, initialPolicy())
	cases := []struct{ provider, newer, older string }{
		{"anthropic", "claude-opus-5-5", "claude-opus-5"},
		{"anthropic", "claude-opus-6", "claude-opus-5-5"},
		{"openai", "gpt-6.1-sol", "gpt-6-sol"},
		{"openai", "gpt-10-sol", "gpt-9-sol"},
		{"moonshotai", "kimi-k2.7", "kimi-k2"},
		{"ollama-cloud", "glm-5.3", "glm-5.2"},
	}
	for _, c := range cases {
		n := mapOne(t, m, c.provider, c.newer, "")
		o := mapOne(t, m, c.provider, c.older, "")
		got, err := modelidentity.Compare(n, o)
		require.NoError(t, err, c.newer)
		assert.Equal(t, 1, got, "%s > %s", c.newer, c.older)
	}
	// 5 and 5.0 are the same version; with no dates they cannot be ordered.
	a := mapOne(t, m, "anthropic", "claude-opus-5", "")
	b := mapOne(t, m, "anthropic", "claude-opus-5-0", "")
	_, err := modelidentity.Compare(a, b)
	assert.ErrorIs(t, err, modelidentity.ErrUnorderable)
}

// Equal numeric versions are ordered by release date, and only by release date.
func TestIdentityReleaseDateIsFed(t *testing.T) {
	m := newMapper(t, initialPolicy())
	// The same version offered under two ids that parse to the same tuple.
	x := mapOne(t, m, "anthropic", "claude-opus-5", "2026-03-01")
	y := mapOne(t, m, "anthropic", "claude-opus-5-0", "2026-06-01")
	got, err := modelidentity.Compare(y, x)
	require.NoError(t, err)
	assert.Equal(t, 1, got)

	// Swap the dates: the order must follow them.
	x = mapOne(t, m, "anthropic", "claude-opus-5", "2026-09-01")
	y = mapOne(t, m, "anthropic", "claude-opus-5-0", "2026-06-01")
	got, err = modelidentity.Compare(y, x)
	require.NoError(t, err)
	assert.Equal(t, -1, got)

	// A higher version wins even with an older date.
	hi := mapOne(t, m, "anthropic", "claude-opus-5-1", "2026-01-01")
	lo := mapOne(t, m, "anthropic", "claude-opus-5", "2026-09-01")
	got, err = modelidentity.Compare(hi, lo)
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}

func TestIdentityEqualVersionMissingOrTiedDateIsUnorderable(t *testing.T) {
	m := newMapper(t, initialPolicy())
	for _, dates := range [][2]string{{"", ""}, {"2026-06-01", ""}, {"2026-06-01", "2026-06-01"}, {"2026-06", "2026-06-15"}} {
		x := mapOne(t, m, "anthropic", "claude-opus-5", dates[0])
		y := mapOne(t, m, "anthropic", "claude-opus-5-0", dates[1])
		_, err := modelidentity.Compare(x, y)
		assert.ErrorIs(t, err, modelidentity.ErrUnorderable, "%v", dates)
	}
}

// Failure: nothing unknown, foreign or cross-tier is ever orderable.
func TestIdentityCompareRefusesUnevidencedPairs(t *testing.T) {
	m := newMapper(t, initialPolicy())
	opus := mapOne(t, m, "anthropic", "claude-opus-5-5", "2026-06-01")
	sonnet := mapOne(t, m, "anthropic", "claude-sonnet-5-5", "2026-06-01")
	unknown := mapOne(t, m, "anthropic", "claude-opus-5-5-fast", "2026-06-01")
	other := mapOne(t, m, "openrouter", "deepseek/deepseek-v4-flash", "2026-06-01")
	flash := mapOne(t, m, "ollama-cloud", "deepseek-v4-flash", "2026-06-01")

	for name, pair := range map[string][2]modelidentity.Entry{
		"cross tier":       {opus, sonnet},
		"unknown vs known": {unknown, opus},
		"unknown vs self":  {unknown, unknown},
		"cross provider":   {other, flash},
		"zero value":       {{}, opus},
	} {
		_, err := modelidentity.Compare(pair[0], pair[1])
		assert.ErrorIs(t, err, modelidentity.ErrUnorderable, name)
	}
}

func TestIdentityExcludedVariantsAreCataloguedNotFollowable(t *testing.T) {
	m := newMapper(t, initialPolicy())
	for _, id := range []string{
		"claude-haiku-4-5-20251001", "deepseek-v4-flash:0731", "claude-opus-5-5-fast",
		"claude-opus-5-5-preview", "glm-5.2-vision-exp", "gpt-6-sol-turbo", "latest",
	} {
		provider := "anthropic"
		switch {
		case id == "deepseek-v4-flash:0731":
			provider = "deepseek"
		case id == "glm-5.2-vision-exp":
			provider = "zhipuai"
		case id == "gpt-6-sol-turbo":
			provider = "openai"
		}
		e := mapOne(t, m, provider, id, "")
		assert.Empty(t, e.Tier, id)
		assert.Empty(t, e.OrderKey, id)
		assert.Equal(t, id, e.NativeID, "unknown entries stay visible with their native id")
		assert.NotEmpty(t, e.CanonicalID, id)
	}
}

func TestIdentityDatedSnapshotKeepsExactVersionButIsNotFollowable(t *testing.T) {
	m := newMapper(t, initialPolicy())
	for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
		for _, suffix := range []string{"4-20250514", "4-0731", "4-5-20251001"} {
			id := "claude-" + tier + "-" + suffix
			t.Run(id, func(t *testing.T) {
				e := mapOne(t, m, "anthropic", id, "2025-10-01")
				assert.Empty(t, e.Tier, "a date must never be captured as a version component")
				assert.Empty(t, e.OrderKey)
				assert.Equal(t, id, e.NativeID)
				assert.Equal(t, id, e.SpecificModel, "a dated snapshot may be pinned without being followable")
				assert.True(t, e.Immutable)
				_, err := modelidentity.Compare(e, mapOne(t, m, "anthropic", "claude-"+tier+"-4-5", ""))
				assert.ErrorIs(t, err, modelidentity.ErrUnorderable)
			})
		}
	}
}

// Edge: only a recognized lab prefix is stripped, and only once.
func TestIdentityPrefixHandling(t *testing.T) {
	m := newMapper(t, initialPolicy())

	multi := mapOne(t, m, "openrouter", "deepseek/deepseek/deepseek-v4-flash", "")
	assert.Empty(t, multi.Tier, "a multi-slash id is not unwrapped repeatedly")

	foreign := mapOne(t, m, "openrouter", "someone/deepseek-v4-flash", "")
	assert.Empty(t, foreign.Tier, "an unrecognized prefix is never stripped")

	// The provider's own native catalog does not use a prefix.
	prefixed := mapOne(t, m, "anthropic", "anthropic/claude-opus-5-5", "")
	assert.Equal(t, "opus", prefixed.Tier)

	// An aggregator that is not a listed carrier for the family stays separate.
	orRouter := mapOne(t, m, "openrouter", "anthropic/claude-opus-5-5", "")
	direct := mapOne(t, m, "anthropic", "claude-opus-5-5", "")
	assert.NotEqual(t, direct.CanonicalID, orRouter.CanonicalID, "unrelated providers are not merged by similar names")
	assert.Empty(t, orRouter.Tier, "openrouter has no anthropic rule in the shipped table")
}

// Routing prefixes belong to the catalog provider or one of its recorded lab
// families; editable rules cannot make an unrelated prefix a routing alias.
func TestIdentityRoutingPrefixes(t *testing.T) {
	m := newMapper(t, initialPolicy())
	for _, tc := range []struct{ provider, native, prefixed string }{
		{"anthropic", "claude-opus-5-5", "anthropic/claude-opus-5-5"},
		{"openai", "gpt-6.1-sol", "openai/gpt-6.1-sol"},
		{"deepseek", "deepseek-v4-flash", "deepseek/deepseek-v4-flash"},
		{"ollama-cloud", "deepseek-v4-flash", "ollama-cloud/deepseek-v4-flash"},
		{"ollama-cloud", "glm-5.3", "ollama-cloud/glm-5.3"},
		{"ollama-cloud", "kimi-k3", "ollama-cloud/kimi-k3"},
		{"openrouter", "deepseek-v4-flash", "deepseek/deepseek-v4-flash"},
	} {
		t.Run(tc.prefixed, func(t *testing.T) {
			bare := mapOne(t, m, tc.provider, tc.native, "")
			prefixed := mapOne(t, m, tc.provider, tc.prefixed, "")
			assert.Equal(t, bare.CanonicalID, prefixed.CanonicalID)
			assert.Equal(t, bare.Tier, prefixed.Tier)
			assert.Equal(t, bare.OrderKey, prefixed.OrderKey)
			assert.Equal(t, tc.provider, prefixed.Provider)
			assert.Equal(t, tc.prefixed, prefixed.NativeID)
			assert.Equal(t, tc.prefixed, prefixed.SpecificModel)
			assert.True(t, prefixed.Immutable)
		})
	}
}

func TestIdentityForeignRoutingPrefixesRemainUnknown(t *testing.T) {
	src := initialPolicy()
	m := newMapper(t, src)
	for _, tc := range []struct{ provider, native string }{
		{"openrouter", "openai/deepseek-v4-flash"},
		{"deepseek", "anthropic/deepseek-v4-flash"},
		{"ollama-cloud", "deepseek/glm-5.3"},
		{"ollama-cloud", "moonshotai/deepseek-v4-flash"},
		{"ollama-cloud", "ollama-cloud/ollama-cloud/deepseek-v4-flash"},
	} {
		t.Run(tc.provider+"/"+tc.native, func(t *testing.T) {
			e := mapOne(t, m, tc.provider, tc.native, "")
			assert.Empty(t, e.Tier)
			assert.Empty(t, e.OrderKey)
			assert.Empty(t, e.SpecificModel)
			assert.False(t, e.Immutable)
			assert.Equal(t, tc.provider+"/"+tc.native, e.CanonicalID)
			assert.Equal(t, tc.native, e.NativeID)
		})
	}

	before := mapOne(t, m, "openrouter", "rogue/deepseek-v4-flash", "")
	src.policy.Revision++
	src.policy.Rules = append(src.policy.Rules, modelidentity.PatternRule{
		Providers: []string{"rogue"}, Tier: "custom", Pattern: `custom-(\d+)`, VersionGroups: []int{1},
	})
	after := mapOne(t, m, "openrouter", "rogue/deepseek-v4-flash", "")
	assert.Equal(t, before.CanonicalID, after.CanonicalID)
	assert.Empty(t, after.Tier, "an unrelated policy provider must not authorize prefix stripping")
	assert.Empty(t, after.OrderKey)
}

// Canonical identity must not depend on the editable tier patterns.
func TestIdentityCanonicalIDIgnoresPolicyEdits(t *testing.T) {
	src := initialPolicy()
	m := newMapper(t, src)
	before := mapOne(t, m, "ollama-cloud", "deepseek-v4-flash", "")

	src.policy = modelidentity.IdentityPolicy{Revision: 2, Rules: []modelidentity.PatternRule{
		{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}},
	}}
	after := mapOne(t, m, "ollama-cloud", "deepseek-v4-flash", "")
	assert.Equal(t, before.CanonicalID, after.CanonicalID)
	assert.Empty(t, after.Tier, "the edited policy no longer follows it")
	assert.Equal(t, int64(2), after.PolicyRevision)
}

// A policy edit that adds a rule makes a previously unmatched model followable.
func TestIdentityPolicyEditChangesTier(t *testing.T) {
	src := initialPolicy()
	m := newMapper(t, src)
	assert.Empty(t, mapOne(t, m, "anthropic", "claude-mythos-1", "").Tier)

	rules := append(modelidentity.InitialRules(), modelidentity.PatternRule{
		Providers: []string{"anthropic"}, Tier: "mythos", Pattern: `claude-mythos-(\d+)`, VersionGroups: []int{1},
	})
	src.policy = modelidentity.IdentityPolicy{Revision: 2, Rules: rules}
	e := mapOne(t, m, "anthropic", "claude-mythos-1", "")
	assert.Equal(t, "mythos", e.Tier)
	assert.Equal(t, "1", e.OrderKey)
}

func TestIdentityAmbiguousIDIsNeverFirstWins(t *testing.T) {
	src := &staticPolicy{policy: modelidentity.IdentityPolicy{Revision: 1, Rules: []modelidentity.PatternRule{
		{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-(?:opus|x)-(\d+)`, VersionGroups: []int{1}},
		{Providers: []string{"anthropic"}, Tier: "x", Pattern: `claude-(?:x|opus)-(\d+)`, VersionGroups: []int{1}},
	}}}
	e := mapOne(t, newMapper(t, src), "anthropic", "claude-opus-5", "")
	assert.True(t, e.Ambiguous)
	assert.Empty(t, e.Tier)
	assert.Empty(t, e.OrderKey)
}

// Input guard: a policy that matches nothing cannot make an arbitrary id followable.
func TestIdentityEmptyPolicyConfirmsNothing(t *testing.T) {
	src := &staticPolicy{policy: modelidentity.IdentityPolicy{Revision: 1}}
	e := mapOne(t, newMapper(t, src), "anthropic", "claude-opus-5-5", "")
	assert.Empty(t, e.Tier)
	assert.Empty(t, e.OrderKey)
	assert.Equal(t, "claude-opus-5-5", e.NativeID)
}

func TestIdentityPolicyReadFailurePropagates(t *testing.T) {
	boom := errors.New("policy store down")
	src := &staticPolicy{err: boom}
	_, err := newMapper(t, src).Map(context.Background(), modelidentity.CatalogModel{CatalogProvider: "anthropic", NativeID: "claude-opus-5-5"})
	require.ErrorIs(t, err, boom)
}

func TestIdentityMapRejectsMalformedEvidence(t *testing.T) {
	m := newMapper(t, initialPolicy())
	for name, cm := range map[string]modelidentity.CatalogModel{
		"no provider":        {NativeID: "claude-opus-5-5"},
		"no native id":       {CatalogProvider: "anthropic"},
		"whitespace id":      {CatalogProvider: "anthropic", NativeID: "claude opus"},
		"bad release date":   {CatalogProvider: "anthropic", NativeID: "claude-opus-5-5", ReleaseDate: "yesterday"},
		"impossible date":    {CatalogProvider: "anthropic", NativeID: "claude-opus-5-5", ReleaseDate: "2026-13-45"},
		"canonical mismatch": {CatalogProvider: "anthropic", NativeID: "claude-opus-5-5", CanonicalModelID: "deepseek/deepseek-v4-flash"},
		"canonical spaces":   {CatalogProvider: "anthropic", NativeID: "claude-opus-5-5", CanonicalModelID: "a b"},
	} {
		_, err := m.Map(context.Background(), cm)
		assert.ErrorIs(t, err, modelidentity.ErrMalformedEvidence, name)
	}
}

func TestIdentityNormalizesReleaseDatePrecision(t *testing.T) {
	m := newMapper(t, initialPolicy())
	assert.Equal(t, "2026-05", mapOne(t, m, "anthropic", "claude-opus-5-5", "2026-05").ReleaseDate)
	assert.Equal(t, "2026-05-22", mapOne(t, m, "anthropic", "claude-opus-5-5", " 2026-05-22 ").ReleaseDate)
	assert.Empty(t, mapOne(t, m, "anthropic", "claude-opus-5-5", "").ReleaseDate)
}

func TestIdentityUsesSourceCanonicalID(t *testing.T) {
	m := newMapper(t, initialPolicy())
	got, err := m.Map(context.Background(), modelidentity.CatalogModel{
		CatalogProvider: "openrouter", NativeID: "vendor/odd-model", CanonicalModelID: "Vendor/Odd-Model", SourceRef: "models.dev#openrouter",
	})
	require.NoError(t, err)
	assert.Equal(t, "vendor/odd-model", got.CanonicalID)
	assert.Equal(t, "models.dev#openrouter", got.SourceRef)

	// Recognized family: the source id unifies with the fallback form.
	withSource, err := m.Map(context.Background(), modelidentity.CatalogModel{
		CatalogProvider: "openrouter", NativeID: "deepseek/deepseek-v4-flash", CanonicalModelID: "deepseek-v4-flash",
	})
	require.NoError(t, err)
	without := mapOne(t, m, "deepseek", "deepseek-v4-flash", "")
	assert.Equal(t, without.CanonicalID, withSource.CanonicalID)
}

func TestIdentityUnmappedOfferingFallsBackToProviderAndNativeID(t *testing.T) {
	m := newMapper(t, initialPolicy())
	a := mapOne(t, m, "openrouter", "vendor/odd-model", "")
	b := mapOne(t, m, "ollama-cloud", "vendor/odd-model", "")
	assert.Equal(t, "openrouter/vendor/odd-model", a.CanonicalID)
	assert.NotEqual(t, a.CanonicalID, b.CanonicalID)
}

func TestIdentityMapHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &staticPolicy{err: ctx.Err()}
	_, err := newMapper(t, src).Map(ctx, modelidentity.CatalogModel{CatalogProvider: "anthropic", NativeID: "claude-opus-5-5"})
	require.ErrorIs(t, err, context.Canceled)
}

// Claude Code's long-context spelling (`claude-sonnet-4-6[1m]`) is a real,
// executable native id (version.go accepts it): the catalog spelling filter
// must keep it, while still refusing brackets anywhere else, so a remote
// catalog cannot smuggle arbitrary text into a probe's --model argument.
func TestIdentityKeepsLongContextSuffix(t *testing.T) {
	for _, native := range []string{"claude-sonnet-4-6[1m]", "anthropic/claude-sonnet-4.5[1m]", "claude-opus-5-5[1m]"} {
		_, err := modelidentity.CanonicalID("anthropic", native, "")
		assert.NoError(t, err, "%q is an executable Claude id", native)
	}
	e := mapOne(t, newMapper(t, initialPolicy()), "anthropic", "claude-sonnet-4-6[1m]", "2026-02-17")
	assert.Equal(t, "claude-sonnet-4-6[1m]", e.NativeID, "the executable spelling is preserved")

	for _, native := range []string{"claude-sonnet-4-6[", "claude-sonnet-4-6]", "claude[1m]-sonnet", "claude-sonnet-4-6[1m][1m]",
		"claude-sonnet-4-6[]", "claude-sonnet-4-6[1m]x", "claude-sonnet-4-6[$(id)]", "[1m]", "claude-sonnet-4-6[1M]"} {
		_, err := modelidentity.CanonicalID("anthropic", native, "")
		assert.ErrorIs(t, err, modelidentity.ErrUnsupportedNativeID, "%q is outside the spelling allow-list", native)
	}
}
