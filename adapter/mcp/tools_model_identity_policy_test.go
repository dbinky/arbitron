package mcp_test

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func policyToolText(t *testing.T, res *gomcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

type passGate struct{ calls int }

func (g *passGate) WithMutation(ctx context.Context, fn func(context.Context) error) error {
	g.calls++
	return fn(ctx)
}

// countingPolicyStore proves unauthorized calls never touch storage.
type countingPolicyStore struct {
	modelidentity.PolicyStore
	calls int
}

func (s *countingPolicyStore) Snapshot(ctx context.Context) (modelidentity.IdentityPolicy, error) {
	s.calls++
	return s.PolicyStore.Snapshot(ctx)
}

func (s *countingPolicyStore) Replace(ctx context.Context, e int64, r []modelidentity.PatternRule) (modelidentity.IdentityPolicy, error) {
	s.calls++
	return s.PolicyStore.Replace(ctx, e, r)
}

func policyRulesArg(pattern string) []any {
	return []any{map[string]any{
		"providers":      []any{"anthropic"},
		"tier":           "opus",
		"pattern":        pattern,
		"version_groups": []any{float64(1)},
	}}
}

func TestIdentityPolicyMCPRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.db")
	db, err := sqlite.Open(path)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlite.RunMigrations(db))
	store := &countingPolicyStore{PolicyStore: sqlite.NewModelIdentityPolicyRepo(db)}
	svc := modelidentity.NewPolicyService(store)
	gate := &passGate{}
	svc.SetMutationGate(gate)
	srv := adaptmcp.NewServerWithDeps("policy", "test", &adaptmcp.Deps{IdentityPolicy: svc})
	adaptmcp.RegisterModelIdentityPolicyTools(srv)
	admin := adminIdentityCtx(uuid.New())

	// Unauthenticated and non-admin callers never reach the store.
	for _, ctx := range []context.Context{context.Background(), nonAdminIdentityCtx(uuid.New())} {
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"model_identity_policy_get", nil},
			{"model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": policyRulesArg(`claude-opus-(\d+)`)}},
		} {
			assert.True(t, callToolCtx(t, srv, ctx, tc.tool, tc.args).IsError)
		}
	}
	assert.Zero(t, store.calls)
	assert.Zero(t, gate.calls)

	// get returns the shipped D2 table and its revision.
	var got modelidentity.IdentityPolicy
	res := callToolCtx(t, srv, admin, "model_identity_policy_get", nil)
	require.False(t, res.IsError)
	require.NoError(t, json.Unmarshal([]byte(policyToolText(t, res)), &got))
	assert.Equal(t, int64(1), got.Revision)
	assert.Equal(t, modelidentity.InitialRules(), got.Rules)

	// update persists the exact submitted pattern and advances the revision.
	const pattern = `claude-opus-(\d+)`
	res = callToolCtx(t, srv, admin, "model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": policyRulesArg(pattern)})
	require.False(t, res.IsError, policyToolText(t, res))
	require.NoError(t, json.Unmarshal([]byte(policyToolText(t, res)), &got))
	assert.Equal(t, int64(2), got.Revision)
	assert.Equal(t, pattern, got.Rules[0].Pattern)
	assert.Equal(t, 1, gate.calls)

	// Close and reopen the file, rerun migrations, then read the trusted source.
	require.NoError(t, db.Close())
	reopened, err := sqlite.Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, sqlite.RunMigrations(reopened))
	svc = modelidentity.NewPolicyService(sqlite.NewModelIdentityPolicyRepo(reopened))
	svc.SetMutationGate(gate)
	srv = adaptmcp.NewServerWithDeps("policy-reopened", "test", &adaptmcp.Deps{IdentityPolicy: svc})
	adaptmcp.RegisterModelIdentityPolicyTools(srv)
	internal, err := svc.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), internal.Revision)
	require.Len(t, internal.Rules, 1)
	assert.Equal(t, got, internal, "the exact submitted policy survives restart without reseeding")

	// Stale revision conflicts; invalid, malformed and unknown inputs persist nothing.
	for name, args := range map[string]map[string]any{
		"stale revision":   {"expected_revision": 1, "rules": policyRulesArg(`claude-opus-(\d+)-x`)},
		"bad regex":        {"expected_revision": 2, "rules": policyRulesArg(`claude-(`)},
		"wildcard capture": {"expected_revision": 2, "rules": policyRulesArg(`claude-(.+)`)},
		"missing capture":  {"expected_revision": 2, "rules": policyRulesArg(`claude-opus-5`)},
		"empty provider":   {"expected_revision": 2, "rules": []any{map[string]any{"providers": []any{" "}, "tier": "opus", "pattern": pattern, "version_groups": []any{1}}}},
		"empty target":     {"expected_revision": 2, "rules": []any{map[string]any{"providers": []any{"anthropic"}, "tier": " ", "pattern": pattern, "version_groups": []any{1}}}},
		"contradictory duplicate": {"expected_revision": 2, "rules": []any{
			policyRulesArg(pattern)[0],
			map[string]any{"providers": []any{"anthropic"}, "tier": "sonnet", "pattern": pattern, "version_groups": []any{1}},
		}},
		"non-integer rev":    {"expected_revision": 1.5, "rules": policyRulesArg(pattern)},
		"missing revision":   {"rules": policyRulesArg(pattern)},
		"missing rules":      {"expected_revision": 2},
		"rules not array":    {"expected_revision": 2, "rules": "x"},
		"rule not object":    {"expected_revision": 2, "rules": []any{"x"}},
		"unknown rule field": {"expected_revision": 2, "rules": []any{map[string]any{"providers": []any{"a"}, "tier": "t", "pattern": `m-(\d+)`, "version_groups": []any{1}, "extra": 1}}},
		"unknown arg":        {"expected_revision": 2, "rules": policyRulesArg(pattern), "extra": 1},
		"empty rules":        {"expected_revision": 2, "rules": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, callToolCtx(t, srv, admin, "model_identity_policy_update", args).IsError)
			after, err := svc.ReadInternal(context.Background())
			require.NoError(t, err)
			assert.Equal(t, internal, after, "rejected edits preserve rules as well as revision")
		})
	}
}

func TestIdentityPolicyMCPRevisionBeyondInt32(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.db")
	db, err := sqlite.Open(path)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlite.RunMigrations(db))
	// Set up an existing, server-owned int64 revision, not a client-supplied new revision.
	const revision int64 = math.MaxInt32 + 1
	_, err = db.Exec(`UPDATE model_identity_policy SET revision=? WHERE id=1`, revision)
	require.NoError(t, err)
	svc := modelidentity.NewPolicyService(sqlite.NewModelIdentityPolicyRepo(db))
	svc.SetMutationGate(&passGate{})
	srv := adaptmcp.NewServerWithDeps("policy", "test", &adaptmcp.Deps{IdentityPolicy: svc})
	adaptmcp.RegisterModelIdentityPolicyTools(srv)
	admin := adminIdentityCtx(uuid.New())
	res := callToolCtx(t, srv, admin, "model_identity_policy_update", map[string]any{"expected_revision": revision, "rules": policyRulesArg(`claude-opus-(\d+)`)})
	require.False(t, res.IsError, policyToolText(t, res))
	want := modelidentity.IdentityPolicy{Revision: revision + 1, Rules: []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}}}
	for _, invalid := range []any{revision, 0, -1, 1.5, "2147483649", math.Exp2(63)} {
		res = callToolCtx(t, srv, admin, "model_identity_policy_update", map[string]any{"expected_revision": invalid, "rules": policyRulesArg(`claude-opus-(\d+)-changed`)})
		require.True(t, res.IsError, "revision %v", invalid)
		after, err := svc.Snapshot(context.Background())
		require.NoError(t, err)
		assert.Equal(t, want, after)
	}
	require.NoError(t, db.Close())
	reopened, err := sqlite.Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, sqlite.RunMigrations(reopened))
	after, err := sqlite.NewModelIdentityPolicyRepo(reopened).Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, after)
}

func TestIdentityPolicyMCPUnavailableAndNoGate(t *testing.T) {
	admin := adminIdentityCtx(uuid.New())
	srv := adaptmcp.NewServerWithDeps("missing", "test", &adaptmcp.Deps{})
	adaptmcp.RegisterModelIdentityPolicyTools(srv)
	assert.True(t, callToolCtx(t, srv, admin, "model_identity_policy_get", nil).IsError)

	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlite.RunMigrations(db))
	svc := modelidentity.NewPolicyService(sqlite.NewModelIdentityPolicyRepo(db)) // no gate injected
	srv = adaptmcp.NewServerWithDeps("nogate", "test", &adaptmcp.Deps{IdentityPolicy: svc})
	adaptmcp.RegisterModelIdentityPolicyTools(srv)
	assert.True(t, callToolCtx(t, srv, admin, "model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": policyRulesArg(`claude-opus-(\d+)`)}).IsError,
		"an update with no mutation gate must fail, not silently succeed")
	after, err := svc.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, modelidentity.IdentityPolicy{Revision: 1, Rules: modelidentity.InitialRules()}, after)
	assert.False(t, callToolCtx(t, srv, admin, "model_identity_policy_get", nil).IsError)
}
