package workflow_test

import (
	"context"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

type serviceBundleCheck func(context.Context, uuid.UUID, string, string) error

func (f serviceBundleCheck) CheckBundleable(ctx context.Context, id uuid.UUID, name, file string) error {
	return f(ctx, id, name, file)
}
func portableWorkflow() *workflow.Workflow {
	return &workflow.Workflow{Name: "portable", Nodes: []workflow.Node{{ID: "script", Type: workflow.NodeTypeScript, Entrypoint: true, Config: workflow.NodeConfig{Portable: true, ScriptFile: "run.sh"}}}}
}

func TestService_WriteWithoutBundleCheckerFailsClosed(t *testing.T) {
	f := newSGFixture(t, false)
	err := f.svc.Create(ctxWith(sgAlice, false), "portable", portableWorkflow())
	require.Error(t, err)
	require.Contains(t, err.Error(), "SCRIPT_BUNDLE_CHECKER_UNAVAILABLE")
	require.Zero(t, f.repo.writeCount())
	require.Zero(t, f.reg.registerCount())
}

func TestService_WriteRejectsUnportableScriptForWriteOwner(t *testing.T) {
	for _, mode := range []string{"create", "private update", "shared update"} {
		t.Run(mode, func(t *testing.T) {
			f := newSGFixture(t, false)
			ctx := ctxWith(sgAlice, mode == "shared update")
			scope := workflow.Private(sgAlice)
			wantOwner := sgAlice
			if mode == "shared update" {
				scope = workflow.Shared()
				wantOwner = uuid.Nil
			}
			original := portableWorkflow()
			original.Nodes[0].Config.Portable = false
			if mode != "create" {
				f.repo.seed(scope, original)
			}
			calls := 0
			refuse := true
			f.svc.SetScriptBundleChecker(serviceBundleCheck(func(got context.Context, id uuid.UUID, name, file string) error {
				calls++
				require.Same(t, ctx, got)
				require.Equal(t, wantOwner, id)
				require.Equal(t, "portable", name)
				require.Equal(t, "run.sh", file)
				if refuse {
					return workflow.ErrBundleOutsideReference
				}
				return nil
			}))
			write := func() error {
				if mode == "create" {
					return f.svc.Create(ctx, "portable", portableWorkflow())
				}
				return f.svc.Update(ctx, "portable", portableWorkflow())
			}
			err := write()
			require.Error(t, err)
			require.Contains(t, err.Error(), "SCRIPT_NOT_PORTABLE")
			require.Equal(t, 1, calls)
			require.Zero(t, f.repo.writeCount())
			require.Zero(t, f.reg.registerCount())
			if mode == "create" {
				require.Nil(t, f.repo.stored(scope, "portable"))
			} else {
				require.False(t, f.repo.stored(scope, "portable").Nodes[0].Config.Portable)
			}
			refuse = false
			require.NoError(t, write())
			require.Equal(t, 2, calls)
			require.True(t, f.repo.stored(scope, "portable").Nodes[0].Config.Portable)
		})
	}
}

func TestService_GroupValidateOptions_BindsCallerOwnerForBundles(t *testing.T) {
	f := newSGFixture(t, false)
	ctx := ctxWith(sgAlice, false)
	calls := 0
	f.svc.SetScriptBundleChecker(serviceBundleCheck(func(got context.Context, id uuid.UUID, name, file string) error {
		calls++
		require.Same(t, ctx, got)
		require.Equal(t, sgAlice, id)
		require.Equal(t, "portable", name)
		require.Equal(t, "run.sh", file)
		return nil
	}))
	require.Empty(t, workflow.Validate(portableWorkflow(), nil, nil, nil, f.svc.GroupValidateOptions(ctx)...))
	require.Equal(t, 1, calls)
	findings := workflow.Validate(portableWorkflow(), nil, nil, nil, f.svc.GroupValidateOptions(context.Background())...)
	require.Equal(t, "SCRIPT_BUNDLE_CHECKER_UNAVAILABLE", findings[0].Code)
	require.Equal(t, 1, calls)
}
