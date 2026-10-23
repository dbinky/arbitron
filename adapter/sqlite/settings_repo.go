package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/settings"
)

// SettingsRepo stores model discovery settings separately from run notifications.
type SettingsRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

var _ settings.SystemSettings = (*SettingsRepo)(nil)

func NewSettingsRepo(db *sql.DB) *SettingsRepo {
	return &SettingsRepo{db: db, queries: generated.New(db)}
}

func readSettings(ctx context.Context, q *generated.Queries) (settings.Values, error) {
	row, err := q.ReadSystemSettings(ctx)
	if err != nil {
		return settings.Values{}, fmt.Errorf("reading system settings: %w", err)
	}
	return settings.Values{CatalogURL: row.CatalogUrl, TeamsWebhookURL: row.TeamsWebhookUrl}, nil
}

func (r *SettingsRepo) Read(ctx context.Context) (settings.Values, error) {
	return readSettings(ctx, r.queries)
}

func (r *SettingsRepo) Update(ctx context.Context, patch settings.Patch) (settings.Values, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return settings.Values{}, fmt.Errorf("beginning settings update: %w", err)
	}
	defer tx.Rollback()
	q := r.queries.WithTx(tx)
	// Only write supplied fields, with no pre-write read. SQLite serializes these
	// writes across repository instances; a second independent patch cannot
	// replace a field it omitted with an earlier snapshot.
	for _, field := range []struct {
		key   string
		value *string
	}{
		{"model_catalog.url", patch.CatalogURL},
		{"model_announcements.teams_webhook_url", patch.TeamsWebhookURL},
	} {
		if field.value == nil {
			continue
		}
		rows, err := q.UpdateSystemSetting(ctx, generated.UpdateSystemSettingParams{Key: field.key, Value: *field.value})
		if err != nil {
			return settings.Values{}, fmt.Errorf("updating system settings: %w", err)
		}
		if rows != 1 {
			return settings.Values{}, fmt.Errorf("updating system settings: expected one row for %s, got %d", field.key, rows)
		}
	}
	values, err := readSettings(ctx, q)
	if err != nil {
		return settings.Values{}, err
	}
	if err := tx.Commit(); err != nil {
		return settings.Values{}, fmt.Errorf("committing settings update: %w", err)
	}
	return values, nil
}
