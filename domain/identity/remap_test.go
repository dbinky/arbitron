package identity

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRenamer records each (from,to) it is asked to rename and can be made to
// fail on a chosen 'from'.
type fakeRenamer struct {
	calls   [][2]string
	failOn  string
	failErr error
}

func (f *fakeRenamer) RenameDepartment(_ context.Context, from, to string) (int64, error) {
	f.calls = append(f.calls, [2]string{from, to})
	if f.failOn != "" && from == f.failOn {
		return 0, f.failErr
	}
	return 1, nil
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(nopWriter{}, nil)) }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestReconcileMappings_RenamesEachChangedEntry(t *testing.T) {
	r := &fakeRenamer{}
	cfg := GroupConfig{Mapping: map[string]string{
		"guid-123": "engineering",
		"guid-b":   "sales",
	}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.NoError(t, err)
	assert.ElementsMatch(t, [][2]string{{"guid-123", "engineering"}, {"guid-b", "sales"}}, r.calls)
}

func TestReconcileMappings_SkipsIdentityRenames(t *testing.T) {
	// from == to after normalization => nothing to rewrite, skip the call.
	r := &fakeRenamer{}
	cfg := GroupConfig{Mapping: map[string]string{
		"engineering": "Engineering", // normalizes to engineering==engineering
		"guid-123":    "platform",
	}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.NoError(t, err)
	assert.Equal(t, [][2]string{{"guid-123", "platform"}}, r.calls, "identity rename skipped")
}

func TestReconcileMappings_NilMapping_NoOp(t *testing.T) {
	r := &fakeRenamer{}
	err := ReconcileMappings(context.Background(), GroupConfig{}, r, discardLog())
	require.NoError(t, err)
	assert.Empty(t, r.calls)
}

func TestReconcileMappings_RenamerError_FailsLoudly(t *testing.T) {
	boom := errors.New("db down")
	r := &fakeRenamer{failOn: "guid-123", failErr: boom}
	cfg := GroupConfig{Mapping: map[string]string{"guid-123": "engineering"}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.Error(t, err, "a renamer error must abort reconcile (fail-fast at boot)")
	assert.ErrorIs(t, err, boom)
}

// Robustness: a nil *slog.Logger must not panic even when rows > 0. The code
// guards log != nil before calling log.Info, but this is only effective if
// the guard stays in place — this test enforces the contract.
func TestReconcileMappings_NilLogger_DoesNotPanic(t *testing.T) {
	r := &fakeRenamer{} // returns rows=1 for every call
	cfg := GroupConfig{Mapping: map[string]string{"guid-123": "engineering"}}
	require.NotPanics(t, func() {
		err := ReconcileMappings(context.Background(), cfg, r, nil)
		require.NoError(t, err)
	})
	assert.Equal(t, 1, len(r.calls), "rename still executed with nil logger")
}

func TestReconcileMappings_DropsBlankEntries(t *testing.T) {
	r := &fakeRenamer{}
	cfg := GroupConfig{Mapping: map[string]string{
		"   ":      "engineering", // blank from
		"guid-123": "   ",         // blank to
		"guid-ok":  "platform",
	}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.NoError(t, err)
	assert.Equal(t, [][2]string{{"guid-ok", "platform"}}, r.calls)
}
