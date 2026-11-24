package workflow

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

type bundleCheckFunc func(context.Context, uuid.UUID, string, string) error

func (f bundleCheckFunc) CheckBundleable(ctx context.Context, owner uuid.UUID, name, file string) error {
	return f(ctx, owner, name, file)
}

func TestValidate_PortableScriptCheckErrorFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{ErrBundleOutsideReference, "SCRIPT_NOT_PORTABLE"},
		{ErrBundleUnsupportedFile, "SCRIPT_NOT_PORTABLE"},
		{ErrBundleTooLarge, "SCRIPT_NOT_PORTABLE"},
		{context.Canceled, "SCRIPT_BUNDLE_CHECK_FAILED"},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			checker := bundleCheckFunc(func(context.Context, uuid.UUID, string, string) error { return fmt.Errorf("walk: %w", tc.err) })
			wf := &Workflow{Name: "portable", Nodes: []Node{{ID: "process", Type: NodeTypeScript, Entrypoint: true, Config: NodeConfig{Portable: true, ScriptFile: "run.sh"}}}}
			findings := Validate(wf, nil, nil, nil, WithScriptBundleChecker(context.Background(), uuid.New(), checker))
			requirePortableCode(t, findings, tc.code)
			require.Contains(t, findings[0].Message, tc.err.Error())
		})
	}
}

func TestValidate_PortableScriptCheckerGetsCallerCtxAndOwner(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "caller")
	owner := uuid.New()
	calls := 0
	checker := bundleCheckFunc(func(got context.Context, id uuid.UUID, name, file string) error {
		calls++
		require.Same(t, ctx, got)
		require.Equal(t, owner, id)
		require.Equal(t, "portable", name)
		require.Equal(t, "run.sh", file)
		return nil
	})
	wf := &Workflow{Name: "portable", Nodes: []Node{{ID: "process", Type: NodeTypeScript, Entrypoint: true, Config: NodeConfig{Portable: true, ScriptFile: "run.sh"}}}}
	require.Empty(t, Validate(wf, nil, nil, nil, WithScriptBundleChecker(ctx, owner, checker)))
	require.Equal(t, 1, calls)
}
