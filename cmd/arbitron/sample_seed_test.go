package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

type recordingPublished struct {
	t     *testing.T
	names []string
}

func (p *recordingPublished) Upsert(_ context.Context, name string, _ uuid.UUID, _ string, vis workflow.Visibility, _ string) error {
	if vis != workflow.VisibilityPublic {
		p.t.Errorf("sample published with non-public visibility: %s", vis)
		return nil
	}
	p.names = append(p.names, name)
	return nil
}
func (p *recordingPublished) Delete(context.Context, string) error { return nil }
func (p *recordingPublished) PublisherOf(context.Context, string) (uuid.UUID, bool, error) {
	return uuid.Nil, false, nil
}
func (p *recordingPublished) PublicationOf(context.Context, string) (workflow.Publication, bool, error) {
	return workflow.Publication{}, false, nil
}

func TestSeedSampleWorkflows_BootIntegration(t *testing.T) {
	srcRoot := t.TempDir()
	sampleDir := filepath.Join(srcRoot, "pr-review")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sampleDir, "workflow.json"), []byte(`{"name":"pr-review","nodes":[]}`), 0o644)
	os.WriteFile(filepath.Join(sampleDir, "manager.md"), []byte("m"), 0o644)

	wsRoot := t.TempDir()
	repo := filesystem.NewWorkflowRepo(wsRoot)
	pub := &recordingPublished{t: t}
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := seedSampleWorkflows(context.Background(), true, srcRoot, wsRoot, repo, pub, owner, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	wf, err := repo.Load(context.Background(), workflow.Shared(), "pr-review")
	if err != nil {
		t.Fatalf("load shared sample: %v", err)
	}
	if wf.Name != "pr-review" {
		t.Errorf("wf.Name = %q", wf.Name)
	}
	if len(pub.names) != 1 || pub.names[0] != "pr-review" {
		t.Errorf("published = %v", pub.names)
	}

	// Second run is a no-op (ledger): no duplicate publication.
	if err := seedSampleWorkflows(context.Background(), true, srcRoot, wsRoot, repo, pub, owner, logger); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if len(pub.names) != 1 {
		t.Errorf("second run re-published: %v", pub.names)
	}
}

func TestSeedSampleWorkflows_DisabledIsNoOp(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	if err := seedSampleWorkflows(context.Background(), false, "/whatever", t.TempDir(), repo, &recordingPublished{t: t}, uuid.New(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("disabled seed should be no-op: %v", err)
	}
}
