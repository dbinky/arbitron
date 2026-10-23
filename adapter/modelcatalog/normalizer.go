package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	catalog "github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
)

var _ catalog.CatalogNormalizer = (*Normalizer)(nil)

// Normalizer reads the models.dev provider map and retains source evidence even
// when a model has no known follow tier or fixed executable version.
type Normalizer struct {
	mapper *modelidentity.IdentityMapper
	now    func() time.Time
}

func NewNormalizer(mapper *modelidentity.IdentityMapper, now func() time.Time) (*Normalizer, error) {
	if mapper == nil || now == nil {
		return nil, errors.New("catalog: normalizer requires identity mapper and clock")
	}
	return &Normalizer{mapper: mapper, now: now}, nil
}

func (n *Normalizer) Normalize(ctx context.Context, source string, raw []byte) (catalog.CatalogSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return catalog.CatalogSnapshot{}, err
	}
	sourceURL, err := url.Parse(source)
	if err != nil || sourceURL.Hostname() == "" || (sourceURL.Scheme != "https" && sourceURL.Scheme != "http") {
		return catalog.CatalogSnapshot{}, errors.New("catalog: invalid source URL")
	}
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &providers); err != nil || providers == nil {
		return catalog.CatalogSnapshot{}, errors.New("catalog: expected models.dev provider map")
	}
	// Distinguish a foreign response envelope from a damaged individual provider.
	// Once the envelope is recognized, malformed slices remain explicit failures.
	recognized := len(providers) == 0
	for _, rawProvider := range providers {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rawProvider, &fields) == nil && fields["id"] != nil && fields["models"] != nil {
			recognized = true
			break
		}
	}
	if !recognized {
		return catalog.CatalogSnapshot{}, errors.New("catalog: unsupported catalog format")
	}
	snapshot := catalog.CatalogSnapshot{FetchedAt: n.now()}
	for _, provider := range slices.Sorted(maps.Keys(providers)) {
		if err := ctx.Err(); err != nil {
			return catalog.CatalogSnapshot{}, err
		}
		slice, err := n.normalizeSlice(ctx, sourceURL, provider, providers[provider])
		if err != nil {
			return catalog.CatalogSnapshot{}, err
		}
		snapshot.Slices = append(snapshot.Slices, slice)
	}
	return snapshot, nil
}

func (n *Normalizer) normalizeSlice(ctx context.Context, source *url.URL, provider string, raw json.RawMessage) (catalog.CatalogSlice, error) {
	slice := catalog.CatalogSlice{Provider: provider, Raw: slices.Clone(raw), Status: catalog.SliceStatusInvalid}
	var parsed struct {
		ID     string                     `json:"id"`
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.ID != provider || provider == "" || parsed.Models == nil {
		return slice, nil
	}
	var models []modelidentity.CatalogModel
	var entries []modelidentity.Entry
	for _, native := range slices.Sorted(maps.Keys(parsed.Models)) {
		if err := ctx.Err(); err != nil {
			return catalog.CatalogSlice{}, err
		}
		var row struct {
			ID               string          `json:"id"`
			CanonicalModelID json.RawMessage `json:"canonical_model_id"`
			ReleaseDate      json.RawMessage `json:"release_date"`
		}
		if err := json.Unmarshal(parsed.Models[native], &row); err != nil || row.ID != native {
			return slice, nil
		}
		canonical, err := optionalString(row.CanonicalModelID)
		if err != nil {
			return slice, nil
		}
		date, err := optionalString(row.ReleaseDate)
		if err != nil {
			return slice, nil
		}
		ref := *source
		ref.Fragment = "/" + pointerSegment(provider) + "/models/" + pointerSegment(native)
		ref.RawFragment = ""
		model := modelidentity.CatalogModel{
			CatalogProvider: provider, NativeID: row.ID, CanonicalModelID: canonical,
			ReleaseDate: date, SourceRef: ref.String(),
		}
		entry, err := n.mapper.Map(ctx, model)
		if errors.Is(err, modelidentity.ErrUnsupportedNativeID) {
			// Outside the native-id allow-list: never a probe candidate, but
			// not evidence that the rest of the slice is damaged.
			continue
		}
		if errors.Is(err, modelidentity.ErrMalformedEvidence) {
			return slice, nil
		}
		if err != nil {
			return catalog.CatalogSlice{}, fmt.Errorf("catalog: map identity: %w", err)
		}
		models = append(models, model)
		entries = append(entries, entry)
	}
	slice.Models, slice.Entries, slice.Status = models, entries, catalog.SliceStatusOK
	return slice, nil
}

// Missing metadata is unknown; present metadata must have the schema's type.
func optionalString(raw json.RawMessage) (string, error) {
	if raw == nil {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("catalog: expected string metadata")
	}
	return text, nil
}

// Source references use JSON Pointer escaping, preserving slashes in native IDs.
func pointerSegment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
