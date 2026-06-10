# Sample Workflows Seeding — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `pr-review` and `feature-pipeline` as bundled sample workflows that the binary seeds into the shared scope on boot across every install transport, with an org-install opt-in prompt.

**Architecture:** A pure `domain/samples.Seeder` orchestrates three ports (`SampleSource` reads on-disk samples, `workflow.WorkflowRepository` writes the Shared scope, `workflow.PublishedStore` records public publications) and a `Ledger` for once-per-name idempotency. Filesystem adapters provide the source + ledger. `cmd/arbitron` wires the seeder after the flatstore migration, gated by a new `samples:` config block. Delivery places the `samples/` files into the install hierarchy (release tarball / Docker COPY / make target); the binary reads them from `samples.sourcedir`.

**Tech Stack:** Go 1.25, sqlc/SQLite, koanf config, cosign-signed releases, bash/PowerShell installers, Docker. Module path: `github.com/arbitron/arbitron`.

**Spec:** `docs/superpowers/specs/2026-06-09-sample-workflows-seeding-design.md`

---

## File Structure

**New:**
- `samples/pr-review/`, `samples/feature-pipeline/` — relocated sample trees (source of truth).
- `samples/samples_test.go` — parse/validate test for the relocated samples.
- `domain/samples/ports.go` — `SampleSource` + `Ledger` ports.
- `domain/samples/seeder.go` — `Seeder` service.
- `domain/samples/seeder_test.go` — Seeder unit tests (fakes).
- `adapter/filesystem/sample_source.go` — on-disk `SampleSource`.
- `adapter/filesystem/sample_source_test.go`
- `adapter/filesystem/seed_ledger.go` — `.samples_seeded` `Ledger`.
- `adapter/filesystem/seed_ledger_test.go`
- `cmd/arbitron/sample_seed.go` — boot-time wiring helper.
- `cmd/arbitron/sample_seed_test.go` — boot integration test.

**Modified:**
- `domain/config/config.go` — add `SamplesConfig` + `Samples` field.
- `adapter/config/loader.go` — add `samples.*` defaults.
- `adapter/config/loader_test.go` (or nearest) — parse test.
- `cmd/arbitron/main.go` — call the seed helper after `publishedRepo` is created.
- `.github/workflows/release.yml` — build + upload `arbitron_samples_<version>.tar.gz`.
- `Makefile` — `install-samples` target; ensure `dist`/`release` include the samples tarball.
- `install.sh`, `install.ps1` — `layout_samples`, `seed_samples`, config write, org prompt.
- `Dockerfile` — `COPY samples/`; image config `samples.*`.

---

## Phase 1 — Core seeding (fully testable now)

### Task 1: Relocate the two workflows to `samples/`

**Files:**
- Move: `workspaces/pr-review/` → `samples/pr-review/`
- Move: `workspaces/feature-pipeline/` → `samples/feature-pipeline/`
- Create: `samples/samples_test.go`

- [ ] **Step 1: Move the trees with git**

```bash
cd ~/Repos/arbitron
mkdir -p samples
git mv workspaces/pr-review samples/pr-review
git mv workspaces/feature-pipeline samples/feature-pipeline
```

- [ ] **Step 2: Find and update any references**

```bash
grep -rn "workspaces/pr-review\|workspaces/feature-pipeline" \
  --include='*.go' --include='*.md' --include='*.yaml' --include='*.yml' \
  --include='*.sh' --include='*.json' . | grep -v node_modules
```
Expected: no matches in tracked source. If any appear (docs/config comments), replace `workspaces/` with `samples/` in those files.

- [ ] **Step 3: Write the failing parse/validate test**

Mirror `examples/workflows/native_ralph_loop_test.go`. Create `samples/samples_test.go`:

```go
package samples_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

// eachSample loads a sample workflow from this directory by copying it into a
// temp workspace under users/<owner>/<name>/ and loading via the real repo.
func loadSample(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	owner := uuid.New()
	base := t.TempDir()
	dst := filepath.Join(base, "users", owner.String(), name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dst, os.DirFS(name)); err != nil {
		t.Fatalf("copy sample %q: %v", name, err)
	}
	repo := filesystem.NewWorkflowRepo(base)
	wf, err := repo.Load(context.Background(), workflow.Private(owner), name)
	if err != nil {
		t.Fatalf("load sample %q: %v", name, err)
	}
	return wf
}

func TestSamplesParse(t *testing.T) {
	for _, name := range []string{"pr-review", "feature-pipeline"} {
		wf := loadSample(t, name)
		if wf.Name != name {
			t.Errorf("sample %q: manifest name = %q", name, wf.Name)
		}
		if len(wf.Nodes) == 0 {
			t.Errorf("sample %q: expected nodes, got 0", name)
		}
	}
}
```

- [ ] **Step 4: Run the test, expect PASS**

Run: `go test ./samples/ -run TestSamplesParse -v`
Expected: PASS (proves the relocated trees still parse via the real repo).

- [ ] **Step 5: Commit**

```bash
git add samples/ workspaces/
git commit -m "refactor(samples): relocate pr-review + feature-pipeline to samples/"
```

---

### Task 2: `SamplesConfig` + defaults

**Files:**
- Modify: `domain/config/config.go`
- Modify: `adapter/config/loader.go:23-60` (defaults)
- Test: `adapter/config/loader_test.go`

- [ ] **Step 1: Write the failing config-load test**

Add to `adapter/config/loader_test.go` (match the file's existing test style for loading a temp YAML; the assertions are the new part):

```go
func TestLoad_SamplesBlock(t *testing.T) {
	yaml := []byte("samples:\n  seed: true\n  sourcedir: \"/opt/arbitron/samples\"\n")
	cfg := loadFromYAML(t, yaml) // existing helper in this test file; if absent, write a temp file + Load
	if !cfg.Samples.Seed {
		t.Errorf("Samples.Seed = false, want true")
	}
	if cfg.Samples.SourceDir != "/opt/arbitron/samples" {
		t.Errorf("Samples.SourceDir = %q", cfg.Samples.SourceDir)
	}
}

func TestLoad_SamplesDefaults(t *testing.T) {
	cfg := loadFromYAML(t, []byte("server:\n  port: 9999\n"))
	if !cfg.Samples.Seed {
		t.Errorf("default Samples.Seed = false, want true")
	}
	if cfg.Samples.SourceDir != "" {
		t.Errorf("default Samples.SourceDir = %q, want empty", cfg.Samples.SourceDir)
	}
}
```
If no `loadFromYAML` helper exists, write the temp-file + `Load(path)` inline as the other tests in the file do.

- [ ] **Step 2: Run, expect FAIL**

Run: `go test ./adapter/config/ -run TestLoad_Samples -v`
Expected: FAIL — `cfg.Samples` undefined.

- [ ] **Step 3: Add the struct**

In `domain/config/config.go`, add the field to `Config` (after `Auth`):

```go
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Workspace     WorkspaceConfig
	LLM           LLMConfig
	Logging       LoggingConfig
	Tracing       TracingConfig
	Queue         QueueConfig
	Script        ScriptConfig
	Notifications NotificationsConfig
	Profiles      []ProfileConfig
	Auth          AuthConfig
	Samples       SamplesConfig
}
```

And add the sub-struct near `WorkspaceConfig`:

```go
// SamplesConfig controls boot-time seeding of bundled sample workflows.
type SamplesConfig struct {
	// Seed enables seeding sample workflows from SourceDir into the shared
	// scope at boot. Default true; a no-op when SourceDir is empty/missing.
	Seed bool
	// SourceDir is the absolute path to the on-disk sample source (the install
	// hierarchy's samples/ dir). Empty disables seeding.
	SourceDir string
}
```

- [ ] **Step 4: Add defaults**

In `adapter/config/loader.go` `defaults()` (alongside `"workspace.path"`):

```go
		"samples.seed":      true,
		"samples.sourcedir": "",
```

- [ ] **Step 5: Run, expect PASS**

Run: `go test ./adapter/config/ -run TestLoad_Samples -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domain/config/config.go adapter/config/loader.go adapter/config/loader_test.go
git commit -m "feat(config): add samples.seed + samples.sourcedir"
```

---

### Task 3: `domain/samples` ports + Seeder (happy path)

**Files:**
- Create: `domain/samples/ports.go`
- Create: `domain/samples/seeder.go`
- Test: `domain/samples/seeder_test.go`

- [ ] **Step 1: Write the ports**

`domain/samples/ports.go`:

```go
// Package samples seeds bundled sample workflows into the shared scope at boot.
package samples

import (
	"context"

	"github.com/arbitron/arbitron/domain/workflow"
)

// SampleSource reads the on-disk sample workflow source (the install
// hierarchy's samples/ directory).
type SampleSource interface {
	// List returns the names of available samples (one per source subdir that
	// contains a workflow.json). A missing source dir returns an empty slice,
	// not an error.
	List(ctx context.Context) ([]string, error)
	// Read returns the parsed manifest and the supporting files (relative path
	// -> content) for the named sample.
	Read(ctx context.Context, name string) (*workflow.Workflow, map[string][]byte, error)
}

// Ledger records which sample names have already been seeded, so each sample is
// seeded at most once per workspace (deletions are not undone; new names in a
// later version still seed).
type Ledger interface {
	Seeded(ctx context.Context) (map[string]bool, error)
	Record(ctx context.Context, name string) error
}
```

- [ ] **Step 2: Write the failing happy-path test**

`domain/samples/seeder_test.go`:

```go
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

type createdFile struct{ scope workflow.Scope; name, rel string; content []byte }

type fakeRepo struct {
	created  []string
	files    []createdFile
	deleted  []string
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
// Unused WorkflowRepository methods to satisfy the interface:
func (r *fakeRepo) Load(context.Context, workflow.Scope, string) (*workflow.Workflow, error) { return nil, nil }
func (r *fakeRepo) List(context.Context, workflow.Scope) ([]workflow.WorkflowSummary, error)  { return nil, nil }
func (r *fakeRepo) Update(context.Context, workflow.Scope, string, *workflow.Workflow) error  { return nil }
func (r *fakeRepo) DeleteFile(context.Context, workflow.Scope, string, string) error          { return nil }
func (r *fakeRepo) ListFiles(context.Context, workflow.Scope, string) ([]string, error)        { return nil, nil }
func (r *fakeRepo) Exists(context.Context, workflow.Scope, string) (bool, error)               { return false, nil }
func (r *fakeRepo) SaveNodePositions(context.Context, workflow.Scope, string, map[string]workflow.NodePosition) error { return nil }
func (r *fakeRepo) CopyTree(context.Context, workflow.Scope, string, workflow.Scope, string, bool) error              { return nil }

type pubCall struct{ name, source string; pub uuid.UUID; vis workflow.Visibility; dept string }

type fakePublished struct {
	calls  []pubCall
	upErr  map[string]error
}

func (p *fakePublished) Upsert(_ context.Context, name string, pub uuid.UUID, source string, vis workflow.Visibility, dept string) error {
	if err := p.upErr[name]; err != nil {
		return err
	}
	p.calls = append(p.calls, pubCall{name, source, pub, vis, dept})
	return nil
}
func (p *fakePublished) Delete(context.Context, string) error { return nil }
func (p *fakePublished) PublisherOf(context.Context, string) (uuid.UUID, bool, error) { return uuid.Nil, false, nil }
func (p *fakePublished) PublicationOf(context.Context, string) (workflow.Publication, bool, error) { return workflow.Publication{}, false, nil }

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
```

- [ ] **Step 3: Run, expect FAIL**

Run: `go test ./domain/samples/ -run TestSeed_HappyPath -v`
Expected: FAIL — `samples.NewSeeder` undefined.

- [ ] **Step 4: Write the Seeder**

`domain/samples/seeder.go`:

```go
package samples

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Seeder writes not-yet-seeded sample workflows from a SampleSource into the
// shared scope and records each as a public publication owned by a bootstrap
// user. Each sample is seeded at most once (tracked via the Ledger).
type Seeder struct {
	source    SampleSource
	repo      workflow.WorkflowRepository
	published workflow.PublishedStore
	ledger    Ledger
	owner     uuid.UUID
	logger    *slog.Logger
}

// NewSeeder constructs a Seeder. owner owns the seeded publications.
func NewSeeder(source SampleSource, repo workflow.WorkflowRepository, published workflow.PublishedStore, ledger Ledger, owner uuid.UUID, logger *slog.Logger) *Seeder {
	return &Seeder{source: source, repo: repo, published: published, ledger: ledger, owner: owner, logger: logger}
}

// Seed seeds every not-yet-seeded sample. A malformed sample is skipped + logged
// and NOT recorded (so a fixed source retries later); it does not abort the
// rest. The first infrastructure error aborts and is returned.
func (s *Seeder) Seed(ctx context.Context) error {
	names, err := s.source.List(ctx)
	if err != nil {
		return fmt.Errorf("listing samples: %w", err)
	}
	seeded, err := s.ledger.Seeded(ctx)
	if err != nil {
		return fmt.Errorf("reading seed ledger: %w", err)
	}
	for _, name := range names {
		if seeded[name] {
			continue
		}
		ok, err := s.seedOne(ctx, name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := s.ledger.Record(ctx, name); err != nil {
			return fmt.Errorf("recording seeded %q: %w", name, err)
		}
	}
	return nil
}

// seedOne returns (true, nil) on success, (false, nil) when the sample was
// skipped as malformed, and (false, err) on an infrastructure failure.
func (s *Seeder) seedOne(ctx context.Context, name string) (bool, error) {
	wf, files, err := s.source.Read(ctx, name)
	if err != nil {
		s.logger.Warn("skipping malformed sample", "name", name, "err", err)
		return false, nil
	}
	shared := workflow.Shared()
	if err := s.repo.Create(ctx, shared, name, wf); err != nil {
		return false, fmt.Errorf("creating shared sample %q: %w", name, err)
	}
	for rel, content := range files {
		if err := s.repo.WriteFile(ctx, shared, name, rel, content); err != nil {
			return false, fmt.Errorf("writing sample file %q/%q: %w", name, rel, err)
		}
	}
	if err := s.published.Upsert(ctx, name, s.owner, name, workflow.VisibilityPublic, ""); err != nil {
		// Compensate the fresh write so we never leave an orphaned, unattributed
		// shared copy (mirrors WorkflowService.Publish's fresh-publish rollback).
		if delErr := s.repo.Delete(ctx, shared, name); delErr != nil {
			s.logger.Error("rollback of shared sample failed after publish-record failure", "name", name, "deleteErr", delErr)
		}
		return false, fmt.Errorf("recording publication for %q: %w", name, err)
	}
	return true, nil
}
```

- [ ] **Step 5: Run, expect PASS**

Run: `go test ./domain/samples/ -run TestSeed_HappyPath -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domain/samples/
git commit -m "feat(samples): Seeder + ports, happy path"
```

---

### Task 4: Seeder — idempotency, malformed, empty, rollback

**Files:**
- Test: `domain/samples/seeder_test.go` (add cases)

- [ ] **Step 1: Add the four failing/covering tests**

```go
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
```

- [ ] **Step 2: Run all Seeder tests, expect PASS**

Run: `go test ./domain/samples/ -v`
Expected: PASS (the Task 3 implementation already satisfies these; if any fail, fix `seeder.go`, not the tests).

- [ ] **Step 3: Commit**

```bash
git add domain/samples/seeder_test.go
git commit -m "test(samples): Seeder idempotency, malformed, rollback"
```

---

### Task 5: `adapter/filesystem` SampleSource

**Files:**
- Create: `adapter/filesystem/sample_source.go`
- Test: `adapter/filesystem/sample_source_test.go`

- [ ] **Step 1: Write the failing test**

```go
package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

func writeSample(t *testing.T, root, name, manifest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSampleSource_ListAndRead(t *testing.T) {
	root := t.TempDir()
	writeSample(t, root, "pr-review", `{"name":"pr-review","nodes":[]}`, map[string]string{"manager.md": "m", "sub/x.md": "y"})

	src := filesystem.NewSampleSource(root)
	names, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "pr-review" {
		t.Fatalf("List = %v", names)
	}
	wf, files, err := src.Read(context.Background(), "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name != "pr-review" {
		t.Errorf("wf.Name = %q", wf.Name)
	}
	if string(files["manager.md"]) != "m" || string(files["sub/x.md"]) != "y" {
		t.Errorf("files = %v", files)
	}
	if _, ok := files["workflow.json"]; ok {
		t.Errorf("workflow.json must not be in supporting files")
	}
}

func TestSampleSource_MissingDirIsEmpty(t *testing.T) {
	src := filesystem.NewSampleSource(filepath.Join(t.TempDir(), "nope"))
	names, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List = %v, want empty", names)
	}
}

func TestSampleSource_RejectsTraversal(t *testing.T) {
	src := filesystem.NewSampleSource(t.TempDir())
	if _, _, err := src.Read(context.Background(), "../escape"); err == nil {
		t.Error("expected error for traversal name")
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

Run: `go test ./adapter/filesystem/ -run TestSampleSource -v`
Expected: FAIL — `NewSampleSource` undefined.

- [ ] **Step 3: Implement**

`adapter/filesystem/sample_source.go`:

```go
package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// SampleSource reads bundled sample workflows from a source directory whose
// immediate subdirectories each contain a workflow.json plus supporting files.
type SampleSource struct {
	root string
}

// NewSampleSource roots a SampleSource at dir (the install's samples/ path).
func NewSampleSource(dir string) *SampleSource { return &SampleSource{root: dir} }

// resolve validates name (no traversal/separators) and returns its abs dir.
func (s *SampleSource) resolve(name string) (string, error) {
	if name == "" || name == "." || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid sample name %q", name)
	}
	return filepath.Join(s.root, name), nil
}

// List returns sample names (subdirs containing workflow.json). A missing root
// is an empty list, not an error.
func (s *SampleSource) List(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("reading sample source: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.root, e.Name(), "workflow.json")); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Read parses the manifest and gathers supporting files (relative path ->
// content), excluding workflow.json itself.
func (s *SampleSource) Read(_ context.Context, name string) (*workflow.Workflow, map[string][]byte, error) {
	dir, err := s.resolve(name)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest for %q: %w", name, err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(manifest, &wf); err != nil {
		return nil, nil, fmt.Errorf("parsing manifest for %q: %w", name, err)
	}
	files := map[string][]byte{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "workflow.json" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reading sample files for %q: %w", name, err)
	}
	return &wf, files, nil
}
```

- [ ] **Step 4: Run, expect PASS**

Run: `go test ./adapter/filesystem/ -run TestSampleSource -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/sample_source.go adapter/filesystem/sample_source_test.go
git commit -m "feat(filesystem): SampleSource reads on-disk sample dir"
```

---

### Task 6: `adapter/filesystem` SeedLedger

**Files:**
- Create: `adapter/filesystem/seed_ledger.go`
- Test: `adapter/filesystem/seed_ledger_test.go`

- [ ] **Step 1: Write the failing test**

```go
package filesystem_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

func TestSeedLedger_RecordAndRead(t *testing.T) {
	root := t.TempDir()
	l := filesystem.NewSeedLedger(root)

	seeded, err := l.Seeded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 0 {
		t.Fatalf("fresh ledger = %v", seeded)
	}
	if err := l.Record(context.Background(), "pr-review"); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(context.Background(), "feature-pipeline"); err != nil {
		t.Fatal(err)
	}

	// A fresh ledger over the same root reads both back (persisted to disk).
	seeded, err = filesystem.NewSeedLedger(root).Seeded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !seeded["pr-review"] || !seeded["feature-pipeline"] {
		t.Errorf("seeded = %v", seeded)
	}
}

func TestSeedLedger_RecordIsIdempotent(t *testing.T) {
	root := t.TempDir()
	l := filesystem.NewSeedLedger(root)
	_ = l.Record(context.Background(), "pr-review")
	_ = l.Record(context.Background(), "pr-review")
	seeded, _ := filesystem.NewSeedLedger(root).Seeded(context.Background())
	if len(seeded) != 1 {
		t.Errorf("seeded = %v, want one entry", seeded)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

Run: `go test ./adapter/filesystem/ -run TestSeedLedger -v`
Expected: FAIL — `NewSeedLedger` undefined.

- [ ] **Step 3: Implement**

`adapter/filesystem/seed_ledger.go`:

```go
package filesystem

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// seedLedgerFile is the marker recording seeded sample names (one per line) in
// the workspace root.
const seedLedgerFile = ".samples_seeded"

// SeedLedger persists seeded sample names to <workspace>/.samples_seeded.
type SeedLedger struct {
	path string
}

// NewSeedLedger roots a ledger at workspaceRoot.
func NewSeedLedger(workspaceRoot string) *SeedLedger {
	return &SeedLedger{path: filepath.Join(workspaceRoot, seedLedgerFile)}
}

// Seeded returns the set of recorded names. A missing file is an empty set.
func (l *SeedLedger) Seeded(_ context.Context) (map[string]bool, error) {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("opening seed ledger: %w", err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); name != "" {
			out[name] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading seed ledger: %w", err)
	}
	return out, nil
}

// Record appends name if not already present. Idempotent.
func (l *SeedLedger) Record(ctx context.Context, name string) error {
	seeded, err := l.Seeded(ctx)
	if err != nil {
		return err
	}
	if seeded[name] {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("ensure workspace root: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening seed ledger for append: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, name); err != nil {
		return fmt.Errorf("writing seed ledger: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run, expect PASS**

Run: `go test ./adapter/filesystem/ -run TestSeedLedger -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/filesystem/seed_ledger.go adapter/filesystem/seed_ledger_test.go
git commit -m "feat(filesystem): SeedLedger persists seeded sample names"
```

---

### Task 7: Boot wiring + integration test

**Files:**
- Create: `cmd/arbitron/sample_seed.go`
- Test: `cmd/arbitron/sample_seed_test.go`
- Modify: `cmd/arbitron/main.go` (after `publishedRepo` is created, ~line 423)

- [ ] **Step 1: Write the boot helper**

`cmd/arbitron/sample_seed.go`:

```go
package main

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
)

// seedSampleWorkflows seeds bundled samples into the shared scope when enabled.
// It is a no-op when seed is false or sourceDir is empty. The actual once-per-name
// idempotency is enforced by the SeedLedger in the workspace root.
func seedSampleWorkflows(ctx context.Context, seed bool, sourceDir, workspacePath string, repo workflow.WorkflowRepository, published workflow.PublishedStore, owner uuid.UUID, logger *slog.Logger) error {
	if !seed || sourceDir == "" {
		return nil
	}
	source := filesystem.NewSampleSource(sourceDir)
	ledger := filesystem.NewSeedLedger(workspacePath)
	return samples.NewSeeder(source, repo, published, ledger, owner, logger).Seed(ctx)
}
```

- [ ] **Step 2: Write the failing integration test**

`cmd/arbitron/sample_seed_test.go` — uses the real filesystem repo + a fake published store, drives the helper end-to-end:

```go
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

type recordingPublished struct{ names []string }

func (p *recordingPublished) Upsert(_ context.Context, name string, _ uuid.UUID, _ string, vis workflow.Visibility, _ string) error {
	if vis != workflow.VisibilityPublic {
		return nil
	}
	p.names = append(p.names, name)
	return nil
}
func (p *recordingPublished) Delete(context.Context, string) error { return nil }
func (p *recordingPublished) PublisherOf(context.Context, string) (uuid.UUID, bool, error) { return uuid.Nil, false, nil }
func (p *recordingPublished) PublicationOf(context.Context, string) (workflow.Publication, bool, error) { return workflow.Publication{}, false, nil }

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
	pub := &recordingPublished{}
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := seedSampleWorkflows(context.Background(), true, srcRoot, wsRoot, repo, pub, owner, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Sample landed in shared scope and is loadable.
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
	if err := seedSampleWorkflows(context.Background(), false, "/whatever", t.TempDir(), repo, &recordingPublished{}, uuid.New(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("disabled seed should be no-op: %v", err)
	}
}
```

- [ ] **Step 3: Run, expect FAIL then PASS**

Run: `go test ./cmd/arbitron/ -run TestSeedSampleWorkflows -v`
Expected: FAIL until Step 1's file compiles together; once both exist, PASS.

- [ ] **Step 4: Wire into `main.go`**

In `cmd/arbitron/main.go`, immediately AFTER `publishedRepo := adaptsqlite.NewPublishedWorkflowRepo(db)` (and after `workflowRepo`/`flatMigrateUser` exist), insert:

```go
	// One-time, marker-guarded seeding of bundled sample workflows into the
	// shared scope (public, owned by the default user). No-op when disabled or
	// when no source dir is configured. Runs after the published repo exists so
	// the publication records can be written.
	if err := seedSampleWorkflows(context.Background(), cfg.Samples.Seed, cfg.Samples.SourceDir, cfg.Workspace.Path, workflowRepo, publishedRepo, flatMigrateUser, logger); err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("sample workflow seeding: %w", err)
	}
	logger.Info("sample workflow seeding complete")
```

- [ ] **Step 5: Build + full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 6: Manual boot check on the dev box**

```bash
make build
mkdir -p ~/.arbitron/samples
cp -R samples/pr-review samples/feature-pipeline ~/.arbitron/samples/
# point config at the source, enable seed (idempotent re-run safe)
# (edit ~/.arbitron/config.yaml: samples.seed: true, samples.sourcedir: ~/.arbitron/samples)
rm -f ~/.arbitron/workspaces/.samples_seeded   # allow a fresh seed for the check
cp arbitron ~/.arbitron/bin/arbitron
launchctl kickstart -k gui/$(id -u)/dev.arbitron
sleep 3
curl -fsS http://127.0.0.1:8080/api/v1/workflows | grep -o '"scope":"shared"' | wc -l
```
Expected: the two samples appear with `"scope":"shared"`.

- [ ] **Step 7: Commit**

```bash
git add cmd/arbitron/sample_seed.go cmd/arbitron/sample_seed_test.go cmd/arbitron/main.go
git commit -m "feat(samples): seed samples into shared scope at boot"
```

---

## Phase 2 — Delivery across transports

### Task 8: Release tarball (`release.yml` + `Makefile`)

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `Makefile`

- [ ] **Step 1: Add a `samples-tarball` make target**

In `Makefile` (uses `VERSION` already defined for `dist`):

```make
# Package the bundled sample workflows into a release tarball alongside the
# binaries, so the installer can fetch + verify + extract them.
samples-tarball:
	@mkdir -p dist
	tar -czf dist/arbitron_samples_$(VERSION).tar.gz -C samples .
	@echo "wrote dist/arbitron_samples_$(VERSION).tar.gz"
```

Make `dist` depend on it so `SHA256SUMS` (generated from `dist/arbitron_*` + this file) covers it. Update the `dist` target's tar/sum line to include the samples tarball in the `sha256sum`/`shasum` glob (it already globs `arbitron_*`, which matches `arbitron_samples_*`). Add `samples-tarball` to the `dist` prerequisites:

```make
dist: clean-dist samples-tarball
```

- [ ] **Step 2: Verify locally**

Run: `make dist VERSION=0.0.0-test && tar -tzf dist/arbitron_samples_0.0.0-test.tar.gz | head && grep samples dist/SHA256SUMS`
Expected: tarball lists `pr-review/...` + `feature-pipeline/...`; `SHA256SUMS` has an `arbitron_samples_0.0.0-test.tar.gz` line.

- [ ] **Step 3: Wire into `release.yml`**

In `.github/workflows/release.yml`, in the build job after the binaries are produced and before `SHA256SUMS` is generated, add a step that builds the samples tarball into the same `dist/` dir:

```yaml
      - name: Package sample workflows
        run: |
          mkdir -p dist
          tar -czf "dist/arbitron_samples_${{ steps.version.outputs.version }}.tar.gz" -C samples .
```

Confirm the existing `SHA256SUMS` generation step globs `arbitron_*` (so it includes the samples tarball) and the upload step uploads all of `dist/` (or add `dist/arbitron_samples_*.tar.gz` to the asset list). No change to the cosign step — it signs `SHA256SUMS`, which now covers the tarball.

- [ ] **Step 4: Commit**

```bash
git add Makefile .github/workflows/release.yml
git commit -m "build(release): package + sign samples tarball"
```

---

### Task 9: `install.sh` — layout, seed, prompt

**Files:**
- Modify: `install.sh`
- Test: the repo's bats installer suite (`tests/install/*.bats` or as the suite is named) — add cases mirroring existing ones.

- [ ] **Step 1: Add `layout_samples` resolver**

Next to the other `layout_*` resolvers in `install.sh`:

```bash
# layout_samples — on-disk sample-workflow source dir (mode-aware).
layout_samples() {
  if _is_shared; then printf '%s/samples\n' "$ARB_LIB"; else printf '%s/samples\n' "$(arbitron_home)"; fi
}
```

- [ ] **Step 2: Add `seed_samples` (download + verify + extract)**

Model it on `fetch_binary_to`/`verify_download`. It fetches `arbitron_samples_<version>.tar.gz` from `arbitron_release_base`, checksum-verifies against the already-fetched `SHA256SUMS`, and extracts into `layout_samples`:

```bash
# fetch_samples_to <destdir> — download + checksum-verify the samples tarball
# for the resolved version and extract it into <destdir>. No-op-safe to call
# once; aborts (non-zero) on any download/verify failure.
fetch_samples_to() {
  local destdir="$1" version base tarball tmpdir sumsfile
  version="$(arbitron_resolve_version)" || return 1
  base="$(arbitron_release_base "$version")"
  tarball="arbitron_samples_${version}.tar.gz"
  tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/arbitron-samples.XXXXXX")" || return 1
  trap 'rm -rf "${tmpdir:-}"' RETURN
  sumsfile="$tmpdir/SHA256SUMS"
  if ! curl -fsSL -o "$sumsfile" "${base}/SHA256SUMS"; then
    log_error "could not fetch SHA256SUMS from ${base}"; return 1
  fi
  if ! curl -fsSL -o "$tmpdir/$tarball" "${base}/${tarball}"; then
    log_error "samples download failed: ${base}/${tarball}"; return 1
  fi
  if ! verify_download "$tmpdir/$tarball" "$tarball" "$sumsfile"; then
    log_error "samples tarball verification failed"; return 1
  fi
  mkdir -p "$destdir"
  tar -xzf "$tmpdir/$tarball" -C "$destdir"
}
```

- [ ] **Step 3: Add the org prompt + local default to the config Q&A**

In `run_config_qa` (local) set the default on; in `run_shared_qa` (org) prompt. Add a global `CFG_SEED_SAMPLES` and honor `ARBITRON_INSTALL_SAMPLE_WORKFLOWS`:

```bash
# Local: always on unless explicitly disabled.
CFG_SEED_SAMPLES="${ARBITRON_INSTALL_SAMPLE_WORKFLOWS:-yes}"
```

For the shared path, in `run_shared_qa` add (mirroring the existing `prompt_yes_no` usage):

```bash
  if [ -n "${ARBITRON_INSTALL_SAMPLE_WORKFLOWS:-}" ]; then
    SCFG_SEED_SAMPLES="$ARBITRON_INSTALL_SAMPLE_WORKFLOWS"
  elif prompt_yes_no SAMPLE_WORKFLOWS "Install sample workflows (pr-review, feature-pipeline)?" yes; then
    SCFG_SEED_SAMPLES="yes"
  else
    SCFG_SEED_SAMPLES="no"
  fi
```

- [ ] **Step 4: Emit the `samples:` config block + call seed**

In `write_config_yaml`, append (using `$(layout_samples)` and the chosen flag, mapping yes→true/no→false):

```bash
  local seed_samples="false"
  [ "${CFG_SEED_SAMPLES:-yes}" = "yes" ] && seed_samples="true"
  cat >>"$cfg" <<EOF
samples:
  seed: ${seed_samples}
  sourcedir: "$(layout_samples)"
EOF
```

Do the same in `write_shared_config` with `SCFG_SEED_SAMPLES`. Then in the install flow (`run_fresh` and `run_shared_install`), after `write_*_config`, when the flag is yes:

```bash
  if [ "${CFG_SEED_SAMPLES:-yes}" = "yes" ]; then
    log_info "installing sample workflows"
    fetch_samples_to "$(layout_samples)" || log_warn "sample install failed; continuing without samples"
  fi
```
(Non-fatal: a samples failure must not abort the whole install. The binary's seed step is also a no-op if the dir is empty.)

- [ ] **Step 5: Add bats coverage**

Add tests mirroring the existing installer suite: (a) `layout_samples` returns the expected local/shared path; (b) `write_config_yaml` with `CFG_SEED_SAMPLES=yes` emits `samples:\n  seed: true`; (c) `fetch_samples_to` extracts a stubbed tarball (reuse the suite's curl mock). Run the suite per the repo's documented command (e.g. `make test-install` or `bats tests/install`).

Run: `make test-install` (or the suite's command)
Expected: PASS, including the new cases.

- [ ] **Step 6: Commit**

```bash
git add install.sh tests/install
git commit -m "feat(install.sh): seed samples + org opt-in prompt"
```

---

### Task 10: `install.ps1` — mirror Task 9

**Files:**
- Modify: `install.ps1`

- [ ] **Step 1: Mirror the four pieces in PowerShell**

Add: a `Get-SamplesDir` (mode-aware), a `Get-Samples` that downloads `arbitron_samples_<version>.tar.gz`, checksum-verifies against `SHA256SUMS` (reuse the script's existing hash-verify helper), and `Expand-Archive`/`tar -xzf` into the samples dir; the `samples:` block in the config writer; and the prompt (always-on for the per-user default, ask for any org/shared path the script supports). Honor `$env:ARBITRON_INSTALL_SAMPLE_WORKFLOWS`.

- [ ] **Step 2: Verify**

Run the script's existing Pester/CI check if present, or `pwsh -NoProfile -Command "& { . ./install.ps1 -WhatIf }"`-style smoke per the file's conventions.
Expected: config writer emits the `samples:` block; no syntax errors.

- [ ] **Step 3: Commit**

```bash
git add install.ps1
git commit -m "feat(install.ps1): seed samples + opt-in prompt"
```

---

### Task 11: Dockerfile

**Files:**
- Modify: `Dockerfile`
- Check: `docker-compose.yaml` / image config source

- [ ] **Step 1: COPY samples into the image + set config**

In `Dockerfile`, copy the samples into the image's sample dir (matching the image's `workspace.path`/data layout) and ensure the image's config (or env) sets `samples.seed: true` + `samples.sourcedir`:

```dockerfile
# Bundled sample workflows (seeded into the shared scope on first boot).
COPY samples/ /var/lib/arbitron/samples/
ENV ARBITRON_SAMPLES_SEED=true \
    ARBITRON_SAMPLES_SOURCEDIR=/var/lib/arbitron/samples
```
Adjust the path to the image's actual data dir if different (inspect the existing `Dockerfile`/compose for where workspace lives).

- [ ] **Step 2: Build + boot the image, verify seeding**

```bash
docker build -t arbitron-samples-test .
# run with a throwaway sqlite + workspace, then:
docker run --rm -p 8081:8080 arbitron-samples-test &
sleep 5
curl -fsS http://127.0.0.1:8081/api/v1/workflows | grep -c '"scope":"shared"'
```
Expected: 2 shared workflows. (Skip if the compose default uses Postgres; use a sqlite-config run.)

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "feat(docker): bundle + seed sample workflows"
```

---

### Task 12: `make install-samples` + dev box

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add the dev/manual install target**

```make
# Copy bundled sample workflows into a local sample source dir for a manual /
# dev install. Override DEST to target a shared install's samples dir.
DEST ?= $(HOME)/.arbitron/samples
install-samples:
	@mkdir -p $(DEST)
	cp -R samples/pr-review samples/feature-pipeline $(DEST)/
	@echo "installed samples into $(DEST)"
```

- [ ] **Step 2: Run it on the dev box + reconcile the running instance**

```bash
make install-samples
# ensure config points at it (samples.seed: true, samples.sourcedir: ~/.arbitron/samples)
rm -f ~/.arbitron/workspaces/.samples_seeded
cp arbitron ~/.arbitron/bin/arbitron
launchctl kickstart -k gui/$(id -u)/dev.arbitron
sleep 3
curl -fsS http://127.0.0.1:8080/api/v1/workflows | grep -c '"scope":"shared"'
```
Expected: 2 (samples now seeded into shared; this supersedes the interim private import — optionally remove the private copies under `~/.arbitron/workspaces/users/00000000-0000-0000-0000-000000000001/{pr-review,feature-pipeline}` to avoid duplication).

- [ ] **Step 3: Final full gate**

Run: `make build && go vet ./... && go test ./... && cd dashboard && npx vitest run`
Expected: all green.

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "build: make install-samples for manual/dev seeding"
```

---

## Self-Review

**Spec coverage:**
- Visibility model A (shared/public, bootstrap owner) → Task 3/4/7. ✓
- No embedding; files on disk; binary copies + writes DB rows → Task 3/5/7. ✓
- `samples:` config (`seed`,`sourcedir`) + defaults + env → Task 2. ✓
- Source of truth `samples/` + relocation + ref updates → Task 1. ✓
- Per-name `.samples_seeded` marker (deletes stick, upgrades add) → Task 4/6. ✓
- Release signed tarball → Task 8. ✓
- Installer download+verify+extract + org prompt + local always + env override → Task 9/10. ✓
- Docker COPY + config → Task 11. ✓
- make target + dev reconcile → Task 12. ✓
- Safety rule (seed needs seed&&sourcedir!=""&&exists) → Task 7 helper guard + SampleSource missing-dir-empty (Task 5). ✓
- Test categories (happy/idempotent/partial-failure/empty/malformed) → Task 3/4. ✓

**Placeholder scan:** none — every code step has complete code; installer/PowerShell steps that adapt to existing helpers name the exact helper to mirror.

**Type consistency:** `NewSeeder(source, repo, published, ledger, owner, logger)` consistent across Tasks 3/7. `SampleSource.List/Read`, `Ledger.Seeded/Record`, `seedSampleWorkflows(ctx, seed, sourceDir, workspacePath, repo, published, owner, logger)` consistent. `workflow.Shared()`, `workflow.Private(owner)`, `workflow.VisibilityPublic` match the verified constants. `PublishedStore` satisfied by `*PublishedWorkflowRepo` (Upsert/Delete/PublisherOf/PublicationOf, verified).

**Note for executor:** in `install.sh`, confirm `verify_download`'s signature `verify_download <file> <basename> <sumsfile>` matches usage (verified at `install.sh` review time); the `RETURN`-trap caveat noted in `fetch_binary_to` does not apply here because `fetch_samples_to` does not run under bats functrace in the seed path — but if the suite runs it under `set -T`, switch to the explicit INT/TERM trap + manual cleanup pattern `fetch_binary_to` uses.
