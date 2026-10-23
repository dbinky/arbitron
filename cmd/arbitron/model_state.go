package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/provider"
)

// fingerprintDomain separates model-state fingerprints from any other use of
// the installation key.
const fingerprintDomain = "arbitron/model-state/fingerprint/v1\x00"

// keyVerifierDomain separates the database's key verifier from fingerprints,
// so the stored verifier never equals any path fingerprint.
const keyVerifierDomain = "arbitron/model-state/key-verifier/v1\x00"

// initModelStateCrypto is the Crypto-only form of initModelState.
func initModelStateCrypto(ctx context.Context, db *sql.DB, databasePath string, injectedSecret []byte) (provider.Crypto, error) {
	crypto, _, err := initModelState(ctx, db, databasePath, injectedSecret)
	return crypto, err
}

// initModelState is independent of connection encryption: subscription-
// only installations also need stable model-state fingerprints and attempt
// encryption. In-memory test stacks must explicitly supply their secret. The
// returned closure is an HMAC-SHA256 over the persisted installation key; the
// key itself is never exposed.
func initModelState(ctx context.Context, db *sql.DB, databasePath string, injectedSecret []byte) (provider.Crypto, func([]byte) string, error) {
	queries := generated.New(db)
	initialized, err := queries.ModelStateSecretInitialized(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("reading model-state initialization: %w", err)
	}
	var key []byte
	if databasePath == ":memory:" {
		if injectedSecret == nil {
			return nil, nil, errors.New("in-memory database requires an explicitly injected model-state secret")
		}
		key = append([]byte(nil), injectedSecret...)
	} else {
		key, err = adaptcrypto.LoadOrCreateInstallationSecret(databasePath+".model-state.key", initialized != 0)
		if err != nil {
			return nil, nil, err
		}
	}
	crypto, err := adaptcrypto.NewAESGCM(key)
	if err != nil {
		return nil, nil, fmt.Errorf("building model-state crypto: %w", err)
	}
	if err := recordModelStateKey(ctx, db, key); err != nil {
		return nil, nil, err
	}
	fingerprint := func(input []byte) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(fingerprintDomain))
		mac.Write(input)
		return hex.EncodeToString(mac.Sum(nil))
	}
	return crypto, fingerprint, nil
}

// recordModelStateKey binds the key to this database (G6). The first boot,
// including the first after upgrading an installation that predates the
// verifier, records it; every later boot must present the same key. A
// mismatched or unreadable verifier fails closed before the initialization
// flag is set, so a substituted key is never adopted and never rewritten.
func recordModelStateKey(ctx context.Context, db *sql.DB, key []byte) error {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(keyVerifierDomain))
	verifier := hex.EncodeToString(mac.Sum(nil))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recording model-state initialization: %w", err)
	}
	defer tx.Rollback()
	queries := generated.New(db).WithTx(tx)
	if err := queries.InsertModelStateKeyVerifier(ctx, verifier); err != nil {
		return fmt.Errorf("recording model-state key verifier: %w", err)
	}
	stored, err := queries.ModelStateKeyVerifier(ctx)
	if err != nil {
		return fmt.Errorf("reading model-state key verifier: %w", err)
	}
	if !hmac.Equal([]byte(stored), []byte(verifier)) {
		return errors.New("model-state key does not match this database; restore the key from the same backup as the database")
	}
	if err := queries.MarkModelStateSecretInitialized(ctx); err != nil {
		return fmt.Errorf("recording model-state initialization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recording model-state initialization: %w", err)
	}
	return nil
}
