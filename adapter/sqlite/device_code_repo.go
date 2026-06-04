package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// DeviceCodeRepo implements identity.DeviceCodeRepository backed by SQLite.
//
// SECURITY: the raw device_code (the polling secret) is NEVER stored or received
// here — only its sha256 (device_code_hash). The short user_code is low-entropy
// and is shown to the human approver, so it is stored in clear. Neither the raw
// code nor the hash is ever logged.
type DeviceCodeRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

var _ identity.DeviceCodeRepository = (*DeviceCodeRepo)(nil)

// NewDeviceCodeRepo creates a DeviceCodeRepo using the given database connection.
func NewDeviceCodeRepo(db *sql.DB) *DeviceCodeRepo {
	return &DeviceCodeRepo{db: db, queries: generated.New(db)}
}

func (r *DeviceCodeRepo) Create(ctx context.Context, dc *identity.DeviceCode) error {
	err := r.queries.CreateDeviceCode(ctx, generated.CreateDeviceCodeParams{
		DeviceCodeHash: dc.HashedCode,
		UserCode:       dc.UserCode,
		ExpiresAt:      timeToString(dc.ExpiresAt),
	})
	if err != nil {
		return fmt.Errorf("creating device code: %w", err)
	}
	return nil
}

func (r *DeviceCodeRepo) GetByHash(ctx context.Context, hashedCode string) (*identity.DeviceCode, error) {
	row, err := r.queries.GetDeviceCodeByHash(ctx, hashedCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("device code: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting device code: %w", err)
	}
	return rowToDeviceCode(row)
}

func (r *DeviceCodeRepo) GetByUserCode(ctx context.Context, userCode string) (*identity.DeviceCode, error) {
	row, err := r.queries.GetDeviceCodeByUserCode(ctx, userCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("device code: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting device code: %w", err)
	}
	return rowToDeviceCode(row)
}

func (r *DeviceCodeRepo) Approve(ctx context.Context, userCode string, userID uuid.UUID) error {
	n, err := r.queries.ApproveDeviceCode(ctx, generated.ApproveDeviceCodeParams{
		UserID:   sql.NullString{String: uuidToString(userID), Valid: true},
		UserCode: userCode,
	})
	if err != nil {
		return fmt.Errorf("approving device code: %w", err)
	}
	if n == 0 {
		// Nothing pending to approve (unknown, already approved/denied/consumed).
		return fmt.Errorf("device code: %w", identity.ErrNotFound)
	}
	return nil
}

func (r *DeviceCodeRepo) Deny(ctx context.Context, userCode string) error {
	if _, err := r.queries.DenyDeviceCode(ctx, userCode); err != nil {
		return fmt.Errorf("denying device code: %w", err)
	}
	return nil
}

func (r *DeviceCodeRepo) Consume(ctx context.Context, hashedCode string) error {
	n, err := r.queries.ConsumeDeviceCode(ctx, hashedCode)
	if err != nil {
		return fmt.Errorf("consuming device code: %w", err)
	}
	if n == 0 {
		// Nothing approved to consume (atomic at-most-once guard).
		return fmt.Errorf("device code: %w", identity.ErrNotFound)
	}
	return nil
}

func (r *DeviceCodeRepo) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	n, err := r.queries.DeleteExpiredDeviceCodes(ctx, timeToString(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("deleting expired device codes: %w", err)
	}
	return int(n), nil
}

// rowToDeviceCode maps a generated device_codes row to the domain DeviceCode.
func rowToDeviceCode(row generated.DeviceCode) (*identity.DeviceCode, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("device code: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("device code: %w", err)
	}
	var userID *uuid.UUID
	if row.UserID.Valid {
		uid, err := parseUUID(row.UserID.String)
		if err != nil {
			return nil, fmt.Errorf("device code: %w", err)
		}
		userID = &uid
	}
	return &identity.DeviceCode{
		HashedCode: row.DeviceCodeHash,
		UserCode:   row.UserCode,
		Status:     identity.DeviceCodeStatus(row.Status),
		UserID:     userID,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
	}, nil
}
