// Package checkpoint holds filesystem stores for runner checkpoints.
package checkpoint

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

var _ engine.SessionCheckpointStore = (*SessionStore)(nil)

const sessionRefPrefix = "session:"

// DefaultMaxSessionBundleBytes bounds one stored bundle.
const DefaultMaxSessionBundleBytes int64 = 512 << 20

// ErrSessionBundleTooLarge means a bundle exceeds the store's byte bound.
var ErrSessionBundleTooLarge = errors.New("checkpoint: session bundle exceeds the size limit")

// SessionStore keeps session bundles under <dir>/<run>/<invocation>/ as
// <turn>-<checksum>.data plus a .meta.json written last (its presence marks a
// complete bundle).
type SessionStore struct {
	dir      string
	retain   int
	maxBytes int64
	mu       sync.Mutex
}

// NewSessionStore opens (creating) the store; retain >= 1 bundles are kept
// per invocation (the committed one plus retain-1 older turns).
func NewSessionStore(dir string, retain int) (*SessionStore, error) {
	if dir == "" {
		return nil, errors.New("checkpoint: session store requires a directory")
	}
	if retain < 1 {
		return nil, fmt.Errorf("checkpoint: session bundle retention must be >= 1 (got %d)", retain)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("checkpoint: session store: %w", err)
	}
	return &SessionStore{dir: dir, retain: retain, maxBytes: DefaultMaxSessionBundleBytes}, nil
}

// SetMaxBundleBytes overrides the per-bundle byte bound (n >= 1).
func (s *SessionStore) SetMaxBundleBytes(n int64) {
	if n >= 1 {
		s.mu.Lock()
		s.maxBytes = n
		s.mu.Unlock()
	}
}

type sessionMeta struct {
	Backend        string `json:"backend"`
	SessionID      string `json:"sessionId"`
	HarnessVersion string `json:"harnessVersion"`
	OriginWorkDir  string `json:"originWorkDir"`
	Checksum       string `json:"checksum"`
}

type sessionRef struct {
	run, inv uuid.UUID
	turn     int
	checksum string
}

func (r sessionRef) String() string {
	return fmt.Sprintf("%s%s/%s/%d/%s", sessionRefPrefix, r.run, r.inv, r.turn, r.checksum)
}

func (r sessionRef) base() string { return fmt.Sprintf("%06d-%s", r.turn, r.checksum) }

func validChecksum(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func parseSessionRef(ref string) (sessionRef, error) {
	rest, ok := strings.CutPrefix(ref, sessionRefPrefix)
	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 4 {
		return sessionRef{}, fmt.Errorf("checkpoint: malformed session ref %q", ref)
	}
	run, err1 := uuid.Parse(parts[0])
	inv, err2 := uuid.Parse(parts[1])
	turn, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || turn < 1 || !validChecksum(parts[3]) ||
		run.String() != parts[0] || inv.String() != parts[1] {
		return sessionRef{}, fmt.Errorf("checkpoint: malformed session ref %q", ref)
	}
	return sessionRef{run: run, inv: inv, turn: turn, checksum: parts[3]}, nil
}

func (s *SessionStore) invDir(run, inv uuid.UUID) string {
	return filepath.Join(s.dir, run.String(), inv.String())
}

func (s *SessionStore) paths(r sessionRef) (data, meta string) {
	base := filepath.Join(s.invDir(r.run, r.inv), r.base())
	return base + ".data", base + ".meta.json"
}

// ensureRealDir creates dir (and its run parent) and refuses symlinks on the
// path below the store root.
func (s *SessionStore) ensureRealDir(run, inv uuid.UUID) error {
	for _, p := range []string{filepath.Join(s.dir, run.String()), s.invDir(run, inv)} {
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("checkpoint: %s is not a plain directory", p)
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func readRegular(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("checkpoint: %s is not a regular file", path)
	}
	return os.ReadFile(path)
}

// Put implements engine.SessionCheckpointStore.
func (s *SessionStore) Put(_ context.Context, key engine.SessionKey, b engine.SessionBundle) (string, error) {
	if key.RunID == uuid.Nil || key.TurnIndex < 1 {
		return "", fmt.Errorf("checkpoint: invalid session key %+v", key)
	}
	if err := b.Verify(); err != nil {
		return "", err
	}
	r := sessionRef{run: key.RunID, inv: key.InvocationID, turn: key.TurnIndex, checksum: b.Checksum}
	meta, err := json.Marshal(sessionMeta{Backend: b.Backend, SessionID: b.SessionID, HarnessVersion: b.HarnessVersion,
		OriginWorkDir: b.OriginWorkDir, Checksum: b.Checksum})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(b.Data)) > s.maxBytes {
		return "", fmt.Errorf("%w: %d > %d bytes", ErrSessionBundleTooLarge, len(b.Data), s.maxBytes)
	}
	if err := s.ensureRealDir(r.run, r.inv); err != nil {
		return "", fmt.Errorf("checkpoint: put session: %w", err)
	}
	dataPath, metaPath := s.paths(r)
	if err := writeFileAtomic(dataPath, b.Data); err != nil {
		return "", fmt.Errorf("checkpoint: put session: %w", err)
	}
	if err := writeFileAtomic(metaPath, meta); err != nil {
		os.Remove(dataPath)
		return "", fmt.Errorf("checkpoint: put session: %w", err)
	}
	return r.String(), nil
}

// Get implements engine.SessionCheckpointStore.
func (s *SessionStore) Get(_ context.Context, ref string) (engine.SessionBundle, error) {
	r, err := parseSessionRef(ref)
	if err != nil {
		return engine.SessionBundle{}, err
	}
	dataPath, metaPath := s.paths(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	rawMeta, err := readRegular(metaPath)
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("checkpoint: get session %s: %w", ref, err)
	}
	var m sessionMeta
	if err := json.Unmarshal(rawMeta, &m); err != nil {
		return engine.SessionBundle{}, fmt.Errorf("checkpoint: get session %s: %w", ref, err)
	}
	data, err := readRegular(dataPath)
	if err != nil {
		return engine.SessionBundle{}, fmt.Errorf("checkpoint: get session %s: %w", ref, err)
	}
	b := engine.SessionBundle{Backend: m.Backend, SessionID: m.SessionID, HarnessVersion: m.HarnessVersion,
		OriginWorkDir: m.OriginWorkDir, Checksum: m.Checksum, Data: data}
	if b.Checksum != r.checksum {
		return engine.SessionBundle{}, fmt.Errorf("%w: stored metadata does not match ref", engine.ErrBundleChecksum)
	}
	if err := b.Verify(); err != nil {
		return engine.SessionBundle{}, err
	}
	return b, nil
}

// Prune implements engine.SessionCheckpointStore.
func (s *SessionStore) Prune(_ context.Context, runID, invocationID uuid.UUID, keepRef string) error {
	keep, err := parseSessionRef(keepRef)
	if err != nil {
		return err
	}
	if keep.run != runID || keep.inv != invocationID {
		return fmt.Errorf("checkpoint: keep ref %s does not belong to %s/%s", keepRef, runID, invocationID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.invDir(runID, invocationID)
	if _, metaPath := s.paths(keep); !fileExists(metaPath) {
		return fmt.Errorf("checkpoint: keep ref %s is not stored; refusing to prune", keepRef)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("checkpoint: prune sessions: %w", err)
	}
	bases := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") {
			continue // CollectGarbage reclaims stale temp files by age
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".meta.json"), ".data")
		turnPart, _, ok := strings.Cut(base, "-")
		turn, err := strconv.Atoi(turnPart)
		if !ok || err != nil {
			continue
		}
		bases[base] = turn
	}
	type stored struct {
		base string
		turn int
	}
	var older []stored
	for base, turn := range bases {
		if base != keep.base() && turn < keep.turn {
			older = append(older, stored{base: base, turn: turn})
		}
	}
	sort.Slice(older, func(i, j int) bool {
		if older[i].turn != older[j].turn {
			return older[i].turn > older[j].turn
		}
		return older[i].base > older[j].base
	})
	retained := map[string]bool{keep.base(): true}
	for i := 0; i < len(older) && i < s.retain-1; i++ {
		retained[older[i].base] = true
	}
	for base := range bases {
		if retained[base] {
			continue
		}
		_ = os.Remove(filepath.Join(dir, base+".meta.json")) // meta first: a half-deleted bundle reads as missing
		_ = os.Remove(filepath.Join(dir, base+".data"))
	}
	return nil
}

// CollectGarbage removes complete bundles whose ref is not live and whose
// metadata is older than olderThan, plus temp files older than olderThan. It is
// the startup GC for abandoned uploads; live covers committed refs and
// pending intents/moves (composed by P4 T13 from the database).
func (s *SessionStore) CollectGarbage(ctx context.Context, live func(ref string) bool, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".tmp-") {
			if info.ModTime().Before(olderThan) {
				_ = os.Remove(path)
			}
			return nil
		}
		base, ok := strings.CutSuffix(name, ".meta.json")
		if !ok || !info.ModTime().Before(olderThan) {
			return nil
		}
		rel, err := filepath.Rel(s.dir, filepath.Dir(path))
		if err != nil {
			return err
		}
		parts := strings.Split(rel, string(filepath.Separator))
		turnPart, sum, found := strings.Cut(base, "-")
		if len(parts) != 2 || !found {
			return nil
		}
		turn, err := strconv.Atoi(turnPart)
		if err != nil {
			return nil
		}
		ref := fmt.Sprintf("%s%s/%s/%d/%s", sessionRefPrefix, parts[0], parts[1], turn, sum)
		if _, perr := parseSessionRef(ref); perr != nil || live(ref) {
			return nil
		}
		_ = os.Remove(path)
		_ = os.Remove(filepath.Join(filepath.Dir(path), base+".data"))
		removed++
		return nil
	})
	return removed, err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
