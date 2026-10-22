// Package samples seeds bundled sample workflows into the shared scope at boot.
package samples

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrMalformedSample distinguishes invalid sample content from an unavailable source.
var ErrMalformedSample = errors.New("samples: malformed sample")

// SampleSource reads the on-disk sample workflow source (the install
// hierarchy's samples/ directory).
type SampleSource interface {
	// List returns the names of available samples (one per source subdir that
	// contains a workflow.json). A missing source dir returns an empty slice,
	// not an error.
	List(ctx context.Context) ([]string, error)
	// Read returns the parsed manifest and the supporting files (relative path
	// -> content) for the named sample. Invalid content wraps ErrMalformedSample;
	// infrastructure errors are returned without that marker and abort seeding.
	Read(ctx context.Context, name string) (*workflow.Workflow, map[string][]byte, error)
}

// Ledger records which sample names have already been seeded, so each sample is
// seeded at most once per workspace (deletions are not undone; new names in a
// later version still seed).
type Ledger interface {
	Seeded(ctx context.Context) (map[string]bool, error)
	Record(ctx context.Context, name string) error
}
