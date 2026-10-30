package samples

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/arbitron/arbitron/domain/workflow"
)

// libLedgerPrefix namespaces library files in the seed ledger so they can
// never collide with a workflow sample name.
const libLedgerPrefix = "@lib/"

// SetLibraryStore wires the shared @lib store the library pass seeds into.
// Without it the pass is skipped.
func (s *Seeder) SetLibraryStore(l workflow.LibraryStore) { s.library = l }

// seedLibrary writes each shipped library file into the shared library at most
// once (ledger key "@lib/<rel>"). It never overwrites: a file an admin already
// placed is recorded as seeded and left alone, and a seeded file an admin later
// edited or deleted is not touched. Names failing workflow.ValidLibRel are
// logged, skipped and not recorded. The first infrastructure error aborts.
func (s *Seeder) seedLibrary(ctx context.Context) error {
	files, err := s.source.ReadLibrary(ctx)
	if err != nil {
		return fmt.Errorf("reading sample library: %w", err)
	}
	if len(files) == 0 {
		return nil
	}
	seeded, err := s.ledger.Seeded(ctx)
	if err != nil {
		return fmt.Errorf("reading seed ledger: %w", err)
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	shared := workflow.Shared()
	for _, rel := range rels {
		key := libLedgerPrefix + rel
		if seeded[key] {
			continue
		}
		if err := workflow.ValidLibRel(rel); err != nil {
			s.logger.Warn("skipping invalid library file", "path", rel, "err", err)
			continue
		}
		_, err := s.library.ReadFile(ctx, shared, rel)
		switch {
		case err == nil:
			// Already present: adopt without overwriting.
		case errors.Is(err, workflow.ErrFileNotFound):
			if err := s.library.WriteFile(ctx, shared, rel, files[rel]); err != nil {
				return fmt.Errorf("seeding library file %q: %w", rel, err)
			}
		default:
			return fmt.Errorf("checking library file %q: %w", rel, err)
		}
		if err := s.ledger.Record(ctx, key); err != nil {
			return fmt.Errorf("recording seeded %q: %w", key, err)
		}
	}
	return nil
}
