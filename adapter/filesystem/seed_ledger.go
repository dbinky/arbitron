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
