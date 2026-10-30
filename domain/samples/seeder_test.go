package samples_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

type fakeSource struct {
	names   []string
	listErr error
	wf      map[string]*workflow.Workflow
	files   map[string]map[string][]byte
	readErr map[string]error
	lib     map[string][]byte
	libErr  error
}

func (f *fakeSource) ReadLibrary(context.Context) (map[string][]byte, error) {
	return f.lib, f.libErr
}

func (f *fakeSource) List(context.Context) ([]string, error) { return f.names, f.listErr }
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
	created      []string
	files        []createdFile
	deleted      []string
	createErr    map[string]error
	writeFileErr map[string]error
	exists       map[string]bool
}

func (r *fakeRepo) Create(_ context.Context, s workflow.Scope, name string, _ *workflow.Workflow) error {
	if err := r.createErr[name]; err != nil {
		return err
	}
	r.created = append(r.created, name)
	return nil
}
func (r *fakeRepo) WriteFile(_ context.Context, s workflow.Scope, name, rel string, c []byte) error {
	if err := r.writeFileErr[name]; err != nil {
		return err
	}
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
func (r *fakeRepo) Exists(_ context.Context, _ workflow.Scope, name string) (bool, error) {
	return r.exists[name], nil
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
	// publisherOf maps a published name to its current publisher, simulating an
	// existing publication (used to drive the re-entrant/resume path).
	publisherOf map[string]uuid.UUID
}

func (p *fakePublished) Upsert(_ context.Context, name string, pub uuid.UUID, source string, vis workflow.Visibility, dept string) error {
	if err := p.upErr[name]; err != nil {
		return err
	}
	p.calls = append(p.calls, pubCall{name, source, pub, vis, dept})
	return nil
}
func (p *fakePublished) Delete(context.Context, string) error                   { return nil }
func (p *fakePublished) SetWorkflowID(context.Context, string, uuid.UUID) error { return nil }
func (p *fakePublished) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	if id, ok := p.publisherOf[name]; ok {
		return id, true, nil
	}
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
		readErr: map[string]error{"bad": samples.ErrMalformedSample},
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

// Publisher ownership alone is not proof that this tree is an interrupted seed.
func TestSeed_CrashThenRerun_RejectsUnprovenBootstrapTree(t *testing.T) {
	owner := uuid.New()
	src := &fakeSource{
		names: []string{"pr-review"},
		wf:    map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		files: map[string]map[string][]byte{"pr-review": {"manager.md": []byte("x")}},
	}
	repo := &fakeRepo{
		createErr: map[string]error{"pr-review": workflow.ErrAlreadyExists},
		exists:    map[string]bool{"pr-review": true},
	}
	pub := &fakePublished{publisherOf: map[string]uuid.UUID{"pr-review": owner}}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, repo, pub, led, owner, discardLogger())

	if err := s.Seed(context.Background()); err == nil {
		t.Fatal("expected unproven-tree collision")
	}
	if len(repo.files) != 0 || len(led.recorded) != 0 {
		t.Fatal("unproven tree must not be rewritten or recorded")
	}
}

// Partial trees must be settled from durable evidence, never blindly healed.
func TestSeed_CrashThenRerun_RejectsUnprovenPartialWrite(t *testing.T) {
	owner := uuid.New()
	src := &fakeSource{
		names: []string{"pr-review"},
		wf:    map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		files: map[string]map[string][]byte{"pr-review": {"a.md": []byte("a"), "b.md": []byte("b")}},
	}
	repo := &fakeRepo{
		createErr: map[string]error{"pr-review": workflow.ErrAlreadyExists},
		exists:    map[string]bool{"pr-review": true},
	}
	pub := &fakePublished{publisherOf: map[string]uuid.UUID{"pr-review": owner}}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, repo, pub, led, owner, discardLogger())

	if err := s.Seed(context.Background()); err == nil {
		t.Fatal("expected partial-tree collision")
	}
	if len(repo.files) != 0 || len(led.recorded) != 0 {
		t.Fatal("partial tree must not be adopted")
	}
}

// An existing shared workflow NOT owned by the bootstrap user (a human published
// their own workflow of the same name) must NOT be treated as a resume: Create's
// ErrAlreadyExists surfaces and aborts rather than silently overwriting.
func TestSeed_AlreadyExistsButForeignOwner_Aborts(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	src := &fakeSource{
		names: []string{"pr-review"},
		wf:    map[string]*workflow.Workflow{"pr-review": {Name: "pr-review"}},
		files: map[string]map[string][]byte{"pr-review": {"manager.md": []byte("x")}},
	}
	repo := &fakeRepo{
		createErr: map[string]error{"pr-review": workflow.ErrAlreadyExists},
		exists:    map[string]bool{"pr-review": true},
	}
	pub := &fakePublished{publisherOf: map[string]uuid.UUID{"pr-review": other}}
	led := &fakeLedger{}
	s := samples.NewSeeder(src, repo, pub, led, owner, discardLogger())

	if err := s.Seed(context.Background()); err == nil {
		t.Fatal("expected error when shared name is owned by another user")
	}
	if len(led.recorded) != 0 {
		t.Errorf("must not record a sample owned by another user; recorded=%v", led.recorded)
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

func (r *fakeRepo) CopyTreeStamped(ctx context.Context, src workflow.Scope, sn string, dst workflow.Scope, dn string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	return workflow.ErrNotFound
}

// An unwired seeder must never accept a group, even when the config is valid.
func TestSeed_GroupWithoutCheckersLeavesNoTreePublicationOrLedger(t *testing.T) {
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	pub := &fakePublished{}
	led := &fakeLedger{}
	w := &workflow.Workflow{Name: "group", Nodes: []workflow.Node{{ID: "g", Type: workflow.NodeTypeParallelGroup, Entrypoint: true}}}
	src := &fakeSource{names: []string{"group"}, wf: map[string]*workflow.Workflow{"group": w}}
	require.NoError(t, samples.NewSeeder(src, repo, pub, led, uuid.New(), discardLogger()).Seed(context.Background()))
	exists, err := repo.Exists(context.Background(), workflow.Shared(), "group")
	require.NoError(t, err)
	require.False(t, exists, "group without validation capabilities must be refused before folder creation")
	require.Empty(t, pub.calls)
	require.Empty(t, led.recorded)
}

type seedProfiles map[string]string

func (p seedProfiles) ProfileKind(alias string) (string, bool) { kind, ok := p[alias]; return kind, ok }

func TestSeed_GroupImportsUseIncomingFilesAndRefuseInvalidDefinitions(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, defect := range []string{"valid", "tasks", "profile", "prompt", "child", "child-store"} {
			if form == workflow.GroupFormBranches && (defect == "child" || defect == "child-store") {
				continue
			}
			t.Run(form+"/"+defect, func(t *testing.T) {
				root := t.TempDir()
				repo := filesystem.NewWorkflowRepo(root)
				pub := &fakePublished{}
				led := &fakeLedger{}
				ctx := context.Background()
				child := &workflow.Workflow{Name: "child", Inputs: []workflow.InputField{{Name: "topic", Type: "string", Required: true}}}
				require.NoError(t, repo.Create(ctx, workflow.Shared(), "child", child))
				g := &workflow.GroupConfig{Form: form, Tasks: []workflow.GroupTask{{Key: "a", Entry: "body"}}, CompletionRule: workflow.GroupRuleAllSucceed, UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResultsAndFiles, ResolverProfile: "agent", ResolverPromptFile: "prompts/resolve.md"}
				w := &workflow.Workflow{Name: "group", Nodes: []workflow.Node{{ID: "g", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: g}}, {ID: "body", Type: workflow.NodeTypeTransform}, {ID: "done", Type: workflow.NodeTypeTransform}, {ID: "failed", Type: workflow.NodeTypeTransform}}, Edges: []workflow.Edge{{Source: "g", Target: "body", Output: workflow.GroupPortTask}, {Source: "g", Target: "done", Output: workflow.GroupPortSuccess}, {Source: "g", Target: "failed", Output: workflow.GroupPortFailure}}}
				if form == workflow.GroupFormWorkflows {
					g.Tasks = []workflow.GroupTask{{Key: "a", Workflow: "child"}}
					w.Nodes = append(w.Nodes[:1], w.Nodes[2:]...)
					w.Edges = w.Edges[1:]
				}
				files := map[string][]byte{"prompts/resolve.md": []byte("incoming resolver")}
				switch defect {
				case "tasks":
					g.Tasks = nil
				case "profile":
					g.ResolverProfile = "chat"
				case "prompt":
					files = nil
				case "child":
					g.Tasks[0].Workflow = "missing"
				case "child-store":
					require.NoError(t, os.WriteFile(filepath.Join(root, "shared", "child", "workflow.json"), []byte("{"), 0o600))
				}
				src := &fakeSource{names: []string{"group"}, wf: map[string]*workflow.Workflow{"group": w}, files: map[string]map[string][]byte{"group": files}}
				require.NoError(t, samples.NewSeeder(src, repo, pub, led, uuid.New(), discardLogger(), seedProfiles{"agent": "agent", "chat": "chat"}).Seed(ctx))
				exists, err := repo.Exists(ctx, workflow.Shared(), "group")
				require.NoError(t, err)
				if defect != "valid" {
					require.False(t, exists)
					require.Empty(t, pub.calls)
					require.Empty(t, led.recorded)
					return
				}
				require.True(t, exists)
				stored, err := os.ReadFile(filepath.Join(root, "shared", "group", "prompts", "resolve.md"))
				require.NoError(t, err)
				require.Equal(t, "incoming resolver", string(stored))
				require.Len(t, pub.calls, 1)
				require.Equal(t, []string{"group"}, led.recorded)
			})
		}
	}
}
