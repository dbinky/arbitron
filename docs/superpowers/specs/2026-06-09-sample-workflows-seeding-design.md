# Sample Workflows: ship & seed on fresh install

**Date:** 2026-06-09
**Status:** Approved (brainstorm) — pending implementation plan
**Branch:** `dev-predeploy-testing`

## Problem

Arbitron ships with no workflows. The two flagship native workflows —
`pr-review` and `feature-pipeline` — live tracked under `workspaces/` but are
not delivered to or loaded by a fresh install on any platform. We want them to
ship **as a pair of examples** that are present after a fresh install
everywhere (installer, Docker, manual), with one nuance: an **org/shared
install** must *ask* the operator whether to install the samples; a
**local/personal install** always seeds them.

## Goals

- The two sample workflows are present and runnable after a fresh install on
  every transport: `install.sh`, `install.ps1`, Docker image, manual/dev.
- Samples are visible **org-wide** in a multi-user install (one canonical
  published copy everyone sees), per the chosen visibility model.
- Org/shared installs prompt before installing samples; local installs seed
  automatically ("always there").
- No large workflow JSON embedded in the binary — samples live as files in the
  install hierarchy; the binary reads them from disk.
- Samples are updatable without rebuilding the binary.

## Non-goals

- Re-seeding / overwriting samples a user has edited (seed-if-not-yet-seeded
  only).
- Versioned sample migration / diffing beyond "new names seed on upgrade".
- A UI for managing samples (out of scope; they are ordinary published
  workflows once seeded).
- Wiring the installer download path end-to-end against a *real release* — the
  release tarball is added to `release.yml`, but installer download testing
  remains deferred until the repo is public and a release is cut (the binary
  seeding, Docker, and make-target paths are fully testable now).

## Decisions (from brainstorm)

1. **Visibility model (org):** Shared/published once, visible org-wide. Seeded
   into the `shared/` scope with a `published_workflows` record owned by the
   bootstrap user, `Visibility = public`.
2. **No embedding:** sample files live on disk in the install hierarchy; the
   binary copies them into `shared/` and writes the DB publication rows on boot.
3. **Source location + config surface:** a `samples:` config block with
   `seed` (bool) and `sourcedir` (string). Mode-aware source dir: local
   `~/.arbitron/samples`, shared `<data>/samples` (e.g. `/var/lib/arbitron/samples`).
4. **Repo source of truth:** a new top-level `samples/` directory; the two
   trees are relocated out of `workspaces/`. `examples/workflows/` is unchanged
   (those remain test fixtures).
5. **Installer delivery:** the release gains a cosign-covered
   `arbitron_samples_<version>.tar.gz`; installers download + checksum-verify +
   extract into `sourcedir`.

## Architecture

Boot-time, one-directional data flow:

```
samples/ (repo)  ──delivery──▶  <sourcedir> on disk  ──binary seeds on boot──▶  shared/<name>/  +  published_workflows row
 source of truth          installer / Docker / make      if enabled & not yet seeded     owner = bootstrap user, public
```

### Components

**1. `samples/` (repo, new top-level dir)** — canonical source of truth.
- `samples/pr-review/` and `samples/feature-pipeline/` (manifest +
  supporting files), relocated from `workspaces/`.
- All references to `workspaces/pr-review` / `workspaces/feature-pipeline`
  (config comments, docs, tests) updated to `samples/...`.

**2. `domain/samples` — `Seeder` service (pure orchestration, hex-clean)**
Depends only on ports; unit-testable with fakes.
- New port `SampleSource`:
  - `List(ctx) ([]string, error)` — sample names available in the source.
  - `Read(ctx, name) (*workflow.Workflow, map[string][]byte, error)` — the
    parsed manifest plus the supporting files (relative path → content).
- Existing port `workflow.WorkflowRepository` — `Create` / `WriteFile` into the
  **Shared** scope.
- Existing port `workflow.PublishedStore` — `Upsert(ctx, name, bootstrapUser,
  sourceName=name, VisibilityPublic, department="")`.
- Logic: for each source sample whose name is **not yet recorded as seeded**,
  write the manifest + files into `shared/<name>/`, then upsert the public
  publication record owned by the bootstrap user.
- Failure handling mirrors `WorkflowService.Publish`'s fresh-publish path: if
  the publication `Upsert` fails after a fresh file write, the written shared
  tree is compensated (deleted) to avoid an orphaned, unattributed copy.
- A malformed sample in the source is skipped + logged; it does not abort the
  whole seed.

**3. `adapter/filesystem` — `SampleSource` implementation** — reads `sourcedir`
from disk (lists subdirs containing `workflow.json`, parses, gathers supporting
files). Reuses the existing path-traversal-rejection precedent.

**4. `cmd/arbitron` wiring** — after the flatstore migration (the established
boot slot, `main.go` ~line 417), if `cfg.Samples.Seed` and `cfg.Samples.SourceDir`
is non-empty and exists, construct the filesystem `SampleSource` + `domain/samples.Seeder`
and run it, guarded by the marker (below). Resolve the bootstrap user via the
existing `GetDefaultUser`. Mirrors `migrateFlatStore`'s error-handling style.

### Config

`domain/config.Config` gains a `Samples SamplesConfig`:

```go
type SamplesConfig struct {
    Seed      bool   // seed sample workflows at boot
    SourceDir string // absolute path to on-disk sample source; empty = no seed
}
```

YAML / koanf (env-overridable as `ARBITRON_SAMPLES_SEED`,
`ARBITRON_SAMPLES_SOURCEDIR`):

```yaml
samples:
  seed: true
  sourcedir: "<install>/samples"
```

Defaults (`adapter/config/loader.go`): `samples.seed: true`,
`samples.sourcedir: ""`.

**Safety rule:** seeding runs only when `seed == true` AND `sourcedir != ""`
AND `sourcedir` exists. Otherwise a skip is logged; an empty/missing source is
never an error.

### Idempotency (marker semantics)

A marker file `<workspace>/.samples_seeded` records the **names** already
seeded, one per line. A sample is seeded only if its name is absent from the
marker; after seeding (or recording a no-op), its name is appended.

Consequences:
- A user who deletes a seeded sample keeps it deleted (its name is already
  recorded).
- A newly-shipped sample in a future version seeds on the next boot (its name
  is not yet recorded).

This is a deliberate refinement of the flatstore binary-marker precedent.

## Delivery per transport

**Release (`release.yml`):** after the binary matrix builds, tar `samples/` →
`arbitron_samples_<version>.tar.gz`, add its hash to `SHA256SUMS` (covered by
the existing cosign signature over `SHA256SUMS`), and upload it as a release
asset. No new signing surface.

**Installer (`install.sh` + `install.ps1`):**
- New mode-aware layout resolver `layout_samples` → local `~/.arbitron/samples`,
  shared `<ARB_LIB>/samples`.
- New `seed_samples` step: download the tarball from the release base, run it
  through the existing `verify_download` checksum gate, extract into
  `layout_samples`; write `samples.seed` + `samples.sourcedir` into the
  generated config.
- **Local install:** always extract + `samples.seed: true` (no prompt).
- **Org/shared install:** prompt *"Install sample workflows (pr-review,
  feature-pipeline)?"* → yes ⇒ extract + `seed: true`; no ⇒ skip extract +
  `seed: false`. Env override `ARBITRON_INSTALL_SAMPLE_WORKFLOWS=yes|no` for
  non-interactive/CI.
- Installer download testing deferred (see Non-goals).

**Docker:** `Dockerfile` adds `COPY samples/ <sourcedir>/`; the image config
sets `samples.seed: true` + `sourcedir`. The image always carries the samples.

**Manual/dev:** `make install-samples` copies repo `samples/` → a target
`sourcedir`. The current dev box gets `~/.arbitron/samples/` populated and its
config `samples.sourcedir` pointed at it (replacing the interim private import).

## Testing (strict TDD — 5 scenario categories)

**`domain/samples.Seeder` (fakes for all three ports):**
- Happy path: an absent sample is written to Shared scope and `Upsert` is called
  with the bootstrap user, `sourceName == name`, `VisibilityPublic`, `dept == ""`.
- Idempotent: a name already in the marker is skipped (no writes, no Upsert).
- Partial failure: `Upsert` fails after a fresh file write ⇒ error surfaced and
  the written shared tree is compensated (deleted).
- Empty source: no samples ⇒ no-op, no error.
- Malformed manifest in source: that sample is skipped + logged; other samples
  still seed.

**`adapter/filesystem` SampleSource:** lists sample dirs; reads manifest +
supporting files; rejects path traversal.

**Config:** loader test — `samples.seed` / `samples.sourcedir` parse from YAML
and from env, with defaults applied.

**Marker idempotency:** seeded names recorded; a deleted sample is not
re-seeded; a new name in source seeds on the next run.

**Relocated samples parse/validate:** a test mirroring
`examples/workflows/native_ralph_loop_test.go` proving `samples/pr-review` and
`samples/feature-pipeline` parse and validate against their profile aliases
(`reviewer`/`coder`; `interviewer`/`planner`/`coder`/`assessor`).

## Key references

- Publish flow + rollback invariant: `domain/workflow/service.go:295`
- Ports (`WorkflowRepository.CopyTree/Create/WriteFile`, `PublishedStore.Upsert`,
  `PublisherLookup`): `domain/workflow/ports.go`, `domain/workflow/service.go:17-34`
- Publication record: `domain/workflow/visibility.go:26`
- `published_workflows` schema: `adapter/sqlite/migrations/006_*`, `010_*`
- Upsert impl: `adapter/sqlite/published_workflow_repo.go:109`
- Bootstrap user `00000000-0000-0000-0000-000000000001`,
  `GetDefaultUser`: `adapter/sqlite/migrations/001_*`, `adapter/sqlite/user_repo.go`
- Config struct + defaults + loader: `domain/config/config.go:4`,
  `adapter/config/loader.go:22`
- Boot wiring + flatstore precedent: `cmd/arbitron/main.go:399-431`,
  `cmd/arbitron/flatstore_migrate.go`
- Filesystem workflow repo (scope layout): `adapter/filesystem/workflow_repo.go:26`
- Sample parse/validate test precedent: `examples/workflows/native_ralph_loop_test.go`
