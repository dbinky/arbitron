package renderreview

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
)

// DefaultArtifactGrace is how old an unreferenced capture (or an empty
// revision/attempt directory) must be before the startup reconcile removes it.
//
// Several processes may share one data directory (rolling restarts, a second
// instance pointed at the same volume). A capture exists unreferenced only
// between Render and the worker's stored result, which the worker bounds by its
// job timeout (15 min by default). Rather than a cross-process lock on every
// capture, the reconcile leaves anything modified within the grace period
// alone, so it can never delete a capture another live process is still
// reviewing or about to store.
const DefaultArtifactGrace = 2 * time.Hour

var (
	_ layout.ArtifactReleaser   = (*BrowserRenderer)(nil)
	_ layout.ArtifactReconciler = (*BrowserRenderer)(nil)
)

const capturePrefix = "capture-"

// captureDirOf maps an artifact path to the unique capture directory this
// renderer created for it (ArtifactDir/<revision>/<attempt>/capture-*/<file>),
// or reports false for anything else, including paths outside ArtifactDir.
func (b *BrowserRenderer) captureDirOf(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(b.cfg.ArtifactDir, filepath.Clean(path))
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || !validRevisionDir(parts[0]) || !validAttemptDir(parts[1]) || !validCaptureDir(parts[2]) || parts[3] == "" || parts[3] == ".." {
		return "", false
	}
	return filepath.Join(b.cfg.ArtifactDir, parts[0], parts[1], parts[2]), true
}

func validRevisionDir(name string) bool {
	return safeSegment.MatchString(name) && !strings.Contains(name, "..")
}

func validAttemptDir(name string) bool {
	n, err := strconv.Atoi(name)
	return err == nil && n >= 1 && strconv.Itoa(n) == name
}

func validCaptureDir(name string) bool {
	return strings.HasPrefix(name, capturePrefix) && len(name) > len(capturePrefix) && !strings.ContainsAny(name, `/\`) && name != ".."
}

// Release removes exactly the capture directories the given artifacts live in.
// Paths this renderer did not produce are ignored; it never removes a revision
// or attempt directory that still holds another capture.
func (b *BrowserRenderer) Release(ctx context.Context, artifacts []layout.Artifact) error {
	seen := map[string]bool{}
	var errs []error
	for _, a := range artifacts {
		dir, ok := b.captureDirOf(a.Path)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := removeCapture(dir); err != nil {
			errs = append(errs, err)
			continue
		}
		pruneEmpty(filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}
	return errors.Join(errs...)
}

// removeCapture deletes one capture directory, refusing symlinks.
func removeCapture(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("renderreview: release %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("renderreview: release %s: not a capture directory", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("renderreview: release %s: %w", dir, err)
	}
	return nil
}

// pruneEmpty removes each directory only if it is empty (os.Remove fails
// otherwise). A capture racing this re-creates its parents (see capture).
func pruneEmpty(dirs ...string) {
	for _, d := range dirs {
		if os.Remove(d) != nil {
			return
		}
	}
}

// ReconcileArtifacts removes crash leftovers: capture directories that no
// stored result of their revision cites and that are older than
// DefaultArtifactGrace, then empty revision/attempt directories. A revision
// whose references cannot be read (store outage) is skipped, never purged.
// Entries that are not this renderer's layout are left untouched.
func (b *BrowserRenderer) ReconcileArtifacts(ctx context.Context, referenced layout.ReferencedArtifacts) error {
	revisions, err := os.ReadDir(b.cfg.ArtifactDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("renderreview: reconcile artifacts: %w", err)
	}
	cutoff := b.now().Add(-b.grace())
	removed := 0
	var errs []error
	for _, rev := range revisions {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if !rev.IsDir() || !validRevisionDir(rev.Name()) {
			continue
		}
		refs, err := referenced(ctx, rev.Name())
		if err != nil {
			errs = append(errs, fmt.Errorf("revision %s: %w", rev.Name(), err))
			continue
		}
		keep := map[string]bool{}
		for _, a := range refs {
			if dir, ok := b.captureDirOf(a.Path); ok {
				keep[dir] = true
			}
		}
		revDir := filepath.Join(b.cfg.ArtifactDir, rev.Name())
		attempts, err := os.ReadDir(revDir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		emptiedRev := true
		for _, att := range attempts {
			attDir := filepath.Join(revDir, att.Name())
			if !att.IsDir() || !validAttemptDir(att.Name()) {
				emptiedRev = false
				continue
			}
			captures, err := os.ReadDir(attDir)
			if err != nil {
				errs = append(errs, err)
				emptiedRev = false
				continue
			}
			emptiedAtt := true
			for _, c := range captures {
				dir := filepath.Join(attDir, c.Name())
				if !c.IsDir() || !validCaptureDir(c.Name()) || keep[dir] || !olderThan(dir, cutoff) {
					emptiedAtt = false
					continue
				}
				if err := removeCapture(dir); err != nil {
					errs = append(errs, err)
					emptiedAtt = false
					continue
				}
				removed++
			}
			// An attempt dir we did not empty ourselves may be one another
			// process just created; only touch it once it is old.
			if !emptiedAtt || len(captures) == 0 && !olderThan(attDir, cutoff) || os.Remove(attDir) != nil {
				emptiedRev = false
			}
		}
		if emptiedRev && (len(attempts) > 0 || olderThan(revDir, cutoff)) {
			_ = os.Remove(revDir)
		}
	}
	if removed > 0 {
		b.logger.Info("layout renderer removed unreferenced render captures", "count", removed)
	}
	return errors.Join(errs...)
}

func (b *BrowserRenderer) grace() time.Duration {
	if b.cfg.ArtifactGrace > 0 {
		return b.cfg.ArtifactGrace
	}
	return DefaultArtifactGrace
}

// olderThan reports whether path was last modified before cutoff. Writing a
// screenshot into a capture directory updates the directory's mtime.
func olderThan(path string, cutoff time.Time) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.ModTime().Before(cutoff)
}
