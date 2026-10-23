// Package settings owns persisted catalog and model-announcement settings.
// Run-notification settings remain in domain/config.
package settings

import (
	"context"
	"errors"
)

// ErrInvalid distinguishes safe validation failures from storage faults.
var ErrInvalid = errors.New("invalid system settings")

type Values struct {
	CatalogURL      string
	TeamsWebhookURL string
}

// Patch distinguishes omitted fields from explicit replacements (including clear).
type Patch struct {
	CatalogURL      *string
	TeamsWebhookURL *string
}

type PublicValues struct {
	CatalogURL      string `json:"catalogUrl"`
	TeamsWebhookURL string `json:"teamsWebhookUrl"`
	TeamsConfigured bool   `json:"teamsConfigured"`
}

// InternalReader supplies raw persisted values to trusted runtime consumers.
// Public boundaries use Service.Get and its redacted projection instead.
type InternalReader interface {
	ReadInternal(context.Context) (Values, error)
}

// SystemSettings atomically merges patches and returns the committed values.
type SystemSettings interface {
	Read(context.Context) (Values, error)
	Update(context.Context, Patch) (Values, error)
}
