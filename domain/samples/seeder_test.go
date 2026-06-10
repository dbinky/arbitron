package samples_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
)

// --- fakes ---

type fakeSource struct {
	names   []string
	wf      map[string]*workflow.Workflow
	files   map[string]map[string][]byte
	readErr map[string]error
}

func (f *fakeSource) List(context.Context) ([]string, error) { return f.names, nil }
func (f *fakeSource) Read(_ context.Context, name string) (*workflow.Workflow, map[string][]byte, error) {
	if err := f.readErr[name]; err != nil {
		return nil, nil, err
	}
	return f.wf[name], f.files[name], nil
}

type createdFile struct {
	scope     workflow.Scope
	name, rel string
	content   []byte
}

type fakeRepo struct {
	created   []string
	files     []createdFile
	deleted   []string
	createErr map[string]error
}

func (r *fakeRepo) Create(_ context.Context, s workflow.Scope, name string, _ *workflow.Workflow) error {
	if err := r.createErr[name]; err != nil {
		return err
	}
	r.created = append(r.created, name)
	return nil
}
func (r *fakeRepo) WriteFile(_ context.Context, s workflow.Scope, name, rel string, c []byte) error {
	r.files = append(r.files, createdFile{s, name, rel, c})
	return nil
}
func (r *fakeRepo) Delete(_ context.Context, _ workflow.Scope, name string) error {
	r.deleted = append(r.deleted, name)
	return nil
}
func (r *fakeRepo) Load(context.Context, workflow.Scope, string) (*workflow.Workflow, error) {
	return nil, nil
}
func (r *fakeRepo) List(context.Context, workflow.Scope) ([]workflow.WorkflowSummary, error) {
	return nil, nil
}
func (r *fakeRepo) Update(context.Context, workflow.Scope, string, *workflow.Workflow) error {
	return nil
}
func (r *fakeRepo) DeleteFile(context.Context, workflow.Scope, string, string) error { return nil }
func (r *fakeRepo) ListFiles(context.Context, workflow.Scope, string) ([]string, error) {
	return nil, nil
}
func (r *fakeRepo) Exists(context.Context, workflow.Scope, string) (bool, error) { return false, nil }
func (r *fakeRepo) SaveNodePositions(context.Context, workflow.Scope, string, map[string]workflow.NodePosition) error {
	return nil
}
func (r *fakeRepo) CopyTree(context.Context, workflow.Scope, string, workflow.Scope, string, bool) error {
	return nil
}

type pubCall struct {
	name, source string
	pub          uuid.UUID
	vis          workflow.Visibility
	dept         string
}

type fakePublished struct {
	calls []pubCall
	upErr map[string]error
}

func (p *fakePublished) Upsert(_ context.Context, name string, pub uuid.UUID, source string, vis workflow.Visibility, dept string) error {
	if err := p.upErr[name]; err != nil {
		return err
	}
	p.calls = append(p.calls, pubCall{name, source, pub, vis, dept})
	return nil
}
func (p *fakePublished) Delete(context.Context, string) error { return nil }
func (p *fakePublished) PublisherOf(context.Context, string) (uuid.UUID, bool, error) {
	return uuid.Nil, false, nil
}
func (p *fakePublished) PublicationOf(context.Context, string) (workflow.Publication, bool, error) {
	return workflow.Publication{}, false, nil
}

type fakeLedger struct {
	seeded   map[string]bool
	recorded []string
}

func (l *fakeLedger) Seeded(context.Context) (map[string]bool, error) {
	if l.seeded == nil {
		return map[string]bool{}, nil
	}
	return l.seeded, nil
}
func (l *fakeLedger) Record(_ context.Context, name string) error {
	l.recorded = append(l.recorded, name)
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSeed_HappyPath(t *testing.T) {
	owner := uuid.New()
	src := &fakeSource{
		names: []string{"pr-review"},
		wf:    map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		files: map[string]map[string][]byte{"pr-review": {"manager.md": []byte("x")}},
	}
	repo := &fakeRepo{}
	pub := &fakePublished{}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, repo, pub, led, owner, discardLogger())

	if err := s.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(repo.created) != 1 || repo.created[0] != "pr-review" {
		t.Errorf("created = %v", repo.created)
	}
	if len(repo.files) != 1 || repo.files[0].rel != "manager.md" {
		t.Errorf("files = %+v", repo.files)
	}
	if repo.files[0].scope != workflow.Shared() {
		t.Errorf("write scope = %+v, want Shared", repo.files[0].scope)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(pub.calls))
	}
	c := pub.calls[0]
	if c.name != "pr-review" || c.source != "pr-review" || c.pub != owner ||
		c.vis != workflow.VisibilityPublic || c.dept != "" {
		t.Errorf("publish call = %+v (owner %v)", c, owner)
	}
	if len(led.recorded) != 1 || led.recorded[0] != "pr-review" {
		t.Errorf("recorded = %v", led.recorded)
	}
}

func TestSeed_SkipsAlreadySeeded(t *testing.T) {
	src := &fakeSource{names: []string{"pr-review"}, wf: map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}}}
	repo := &fakeRepo{}
	pub := &fakePublished{}
	led := &fakeLedger{seeded: map[string]bool{"pr-review": true}}
	s := samples.NewSeeder(src, repo, pub, led, uuid.New(), discardLogger())
	if err := s.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.created) != 0 || len(pub.calls) != 0 || len(led.recorded) != 0 {
		t.Errorf("expected no writes; created=%v pub=%d recorded=%v", repo.created, len(pub.calls), led.recorded)
	}
}

func TestSeed_EmptySourceIsNoOp(t *testing.T) {
	s := samples.NewSeeder(&fakeSource{}, &fakeRepo{}, &fakePublished{}, &fakeLedger{}, uuid.New(), discardLogger())
	if err := s.Seed(context.Background()); err != nil {
		t.Fatalf("Seed on empty source: %v", err)
	}
}

func TestSeed_MalformedIsSkippedNotRecorded(t *testing.T) {
	src := &fakeSource{
		names:   []string{"bad", "pr-review"},
		wf:      map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		readErr: map[string]error{"bad": context.DeadlineExceeded},
	}
	repo := &fakeRepo{}
	pub := &fakePublished{}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, repo, pub, led, uuid.New(), discardLogger())
	if err := s.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// "bad" skipped (not created, not recorded); "pr-review" still seeded.
	if len(repo.created) != 1 || repo.created[0] != "pr-review" {
		t.Errorf("created = %v", repo.created)
	}
	for _, n := range led.recorded {
		if n == "bad" {
			t.Errorf("malformed sample was recorded as seeded")
		}
	}
}

func TestSeed_PublishFailureRollsBackAndAborts(t *testing.T) {
	src := &fakeSource{names: []string{"pr-review"}, wf: map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}}}
	repo := &fakeRepo{}
	pub := &fakePublished{upErr: map[string]error{"pr-review": context.Canceled}}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, repo, pub, led, uuid.New(), discardLogger())
	if err := s.Seed(context.Background()); err == nil {
		t.Fatal("expected error from publish failure")
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != "pr-review" {
		t.Errorf("expected rollback delete of pr-review; deleted=%v", repo.deleted)
	}
	if len(led.recorded) != 0 {
		t.Errorf("must not record a sample whose publication failed; recorded=%v", led.recorded)
	}
}
