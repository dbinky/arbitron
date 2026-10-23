// Package modelcatalog defines catalog discovery ports.
package modelcatalog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
)

const (
	SliceStatusOK      = "ok"
	SliceStatusInvalid = "invalid"
)

// CatalogSlice retains the original provider slice and its parsed identity inputs.
// Invalid slices have no Models or Entries and must not replace prior membership.
type CatalogSlice struct {
	Provider string
	Models   []modelidentity.CatalogModel
	Entries  []modelidentity.Entry
	Raw      json.RawMessage
	Status   string
}

// CatalogSnapshot is one normalized source observation, including failed slices.
type CatalogSnapshot struct {
	FetchedAt time.Time
	Slices    []CatalogSlice
}

// CatalogNormalizer parses source-backed model evidence without dropping providers.
type CatalogNormalizer interface {
	Normalize(context.Context, string, []byte) (CatalogSnapshot, error)
}

// CatalogFetcher downloads the raw catalog from the configured source URL.
// Schema validation belongs to the normalizer, not the raw transport.
type CatalogFetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}
