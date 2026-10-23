package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestConnectionAndProfileAdminWritesWaitOnSharedGate drives the real initDomain
// and holds a read lease on the stack's one ExecutionGate (a run-snapshot capture
// or in-flight advancement). Connection and profile admin writes reached through
// the production services must queue behind it and then persist to SQLite.
func TestConnectionAndProfileAdminWritesWaitOnSharedGate(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	ds, err := initDomain(context.Background(), path)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	require.NotNil(t, ds.executionGate)

	ops := map[string]func() error{
		"connection create": func() error {
			return ds.connSvc.Create(admin, provider.Connection{Name: "gate-conn", Provider: "openrouter", BaseURL: "https://openrouter.example/v1"}, "sk-test")
		},
		"profile create": func() error {
			_, _, err := ds.profileSvc.Create(admin, provider.Profile{Alias: "gate-profile", Kind: provider.ProfileKindChat, ConnectionRef: "gate-conn", Model: "deepseek-v3-flash"})
			return err
		},
		"profile delete":    func() error { return ds.profileSvc.Delete(admin, "gate-profile") },
		"connection delete": func() error { return ds.connSvc.Delete(admin, "gate-conn") },
	}
	for _, name := range []string{"connection create", "profile create", "profile delete", "connection delete"} {
		held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			_ = ds.executionGate.WithRead(context.Background(), func(context.Context) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		res := make(chan error, 1)
		go func() { res <- ops[name]() }()
		select {
		case err := <-res:
			close(release)
			<-done
			t.Fatalf("%s ran while the shared gate read lease was held (err=%v): the service is not on the shared gate", name, err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		<-done
		select {
		case err := <-res:
			require.NoError(t, err, name)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never completed after release", name)
		}
	}
	_, err = ds.connSvc.Get(admin, "gate-conn")
	require.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// TestWorkflowServiceWritesWaitOnSharedGate proves the production workflow
// service is on the stack's one ExecutionGate: with a read lease held, a create
// blocks, then persists once the lease is released.
func TestWorkflowServiceWritesWaitOnSharedGate(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	admin := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
	ds, err := initDomain(context.Background(), path)
	require.NoError(t, err)
	defer ds.Close(context.Background())

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = ds.executionGate.WithRead(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	res := make(chan error, 1)
	go func() { res <- ds.workflowSvc.Create(admin, "gated-wf", &workflow.Workflow{Name: "gated-wf"}) }()
	select {
	case err := <-res:
		close(release)
		<-done
		t.Fatalf("workflow create ran while the shared gate read lease was held (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	select {
	case err := <-res:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("workflow create never completed after release")
	}
	_, err = ds.workflowSvc.Load(admin, "gated-wf")
	require.NoError(t, err)
}
