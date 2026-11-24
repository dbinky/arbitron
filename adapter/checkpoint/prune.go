// adapter/checkpoint/prune.go
package checkpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// DefaultPruneGrace is how old an unreferenced file must be before Prune
// removes it. It is far longer than any staging, push or commit takes, so an
// in-flight operation is never raced.
const DefaultPruneGrace = 24 * time.Hour

// WithPruneGrace replaces DefaultPruneGrace (values <= 0 are ignored).
func WithPruneGrace(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.pruneGrace = d
		}
	}
}

// PruneReport counts what one Prune removed.
type PruneReport struct {
	Archives        int   // unreferenced artifact versions (archive + manifest)
	Markers         int   // abandoned version reservations
	CacheEntries    int   // stale read-cache extractions
	ScratchDirs     int   // abandoned staging/tmp directories
	Bytes           int64 // bytes of removed artifacts
	SkippedHeldRuns int   // runs left alone because a writer is active
}

// Prune removes, once older than the grace period: artifacts with no catalog
// row (orphans, e.g. a StageResource whose commit failed), abandoned version
// markers (never the run's highest, so versions are not reused), read-cache
// entries other than the committed pointer's, and abandoned staging/tmp
// directories. It never removes a catalog row or an artifact any row names
// (committed or prepared), and it skips every run with an active writer
// (lease or offered/running job). A catalog error stops Prune before that run
// is touched.
func (s *Store) Prune(ctx context.Context, now time.Time) (PruneReport, error) {
	var rep PruneReport
	if s.catalog == nil {
		return rep, runner.ErrNoCheckpointCatalog
	}
	cutoff := now.Add(-s.pruneGrace)
	for _, kind := range []string{"staging", "tmp"} {
		es, _ := os.ReadDir(filepath.Join(s.dir, kind))
		for _, e := range es {
			p := filepath.Join(s.dir, kind, e.Name())
			if olderThan(p, cutoff) {
				forceRemoveAll(p)
				rep.ScratchDirs++
			}
		}
	}
	runs := map[uuid.UUID]bool{}
	for _, area := range []string{"runs", "cache"} {
		es, _ := os.ReadDir(filepath.Join(s.dir, area))
		for _, e := range es {
			if id, err := uuid.Parse(e.Name()); err == nil && e.IsDir() {
				runs[id] = true
			}
		}
	}
	for id := range runs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if err := s.pruneRun(ctx, id, cutoff, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (s *Store) pruneRun(ctx context.Context, id uuid.UUID, cutoff time.Time, rep *PruneReport) error {
	unlock := s.lockRun(id)
	defer unlock()
	ret, err := s.catalog.Retention(ctx, id)
	if err != nil {
		return fmt.Errorf("checkpoint: prune %s: %w", id, err)
	}
	if ret.Held {
		rep.SkippedHeldRuns++
		return nil
	}
	files := map[int64][]string{}
	var highest int64
	es, _ := os.ReadDir(s.runDir(id))
	for _, e := range es {
		var v int64
		if _, err := fmt.Sscanf(e.Name(), "v%010d", &v); err != nil {
			continue
		}
		files[v] = append(files[v], filepath.Join(s.runDir(id), e.Name()))
		if v > highest {
			highest = v
		}
	}
	for v, paths := range files {
		if ret.Versions[v] {
			continue
		}
		var artifact, marker []string
		for _, p := range paths {
			if strings.HasSuffix(p, ".reserved") {
				marker = append(marker, p)
			} else {
				artifact = append(artifact, p)
			}
		}
		if len(artifact) > 0 && allOlderThan(artifact, cutoff) {
			for _, p := range artifact {
				if fi, err := os.Lstat(p); err == nil && strings.HasSuffix(p, ".tar.gz") {
					rep.Bytes += fi.Size()
				}
				if err := os.Remove(p); err != nil {
					return fmt.Errorf("checkpoint: prune %s: %w", p, err)
				}
			}
			rep.Archives++
			artifact = nil
		}
		if len(artifact) == 0 && v != highest && allOlderThan(marker, cutoff) {
			for _, p := range marker {
				if err := os.Remove(p); err != nil {
					return fmt.Errorf("checkpoint: prune %s: %w", p, err)
				}
				rep.Markers++
			}
		}
	}
	cacheRun := filepath.Join(s.dir, "cache", id.String())
	ces, _ := os.ReadDir(cacheRun)
	for _, e := range ces {
		var v int64
		if _, err := fmt.Sscanf(e.Name(), "v%010d", &v); err == nil && v == ret.Committed {
			continue
		}
		p := filepath.Join(cacheRun, e.Name())
		if olderThan(p, cutoff) {
			forceRemoveAll(p)
			rep.CacheEntries++
		}
	}
	return nil
}

func olderThan(p string, cutoff time.Time) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.ModTime().Before(cutoff)
}

func allOlderThan(ps []string, cutoff time.Time) bool {
	for _, p := range ps {
		if !olderThan(p, cutoff) {
			return false
		}
	}
	return true
}
