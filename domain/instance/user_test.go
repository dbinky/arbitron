package instance

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// User carries a nullable Subject/Email and an identity.Role.
func TestUser_Fields(t *testing.T) {
	sub := "sub-1"
	email := "u@example.com"
	u := User{
		ID:       uuid.New(),
		Subject:  &sub,
		Email:    &email,
		Username: "u",
		Role:     identity.RoleAdmin,
	}
	assert.Equal(t, identity.RoleAdmin, u.Role)
	assert.Equal(t, "sub-1", *u.Subject)
	assert.Equal(t, "u@example.com", *u.Email)

	// Default-user shape: nil subject/email.
	d := User{ID: uuid.New(), Username: "local", Role: identity.RoleAdmin}
	assert.Nil(t, d.Subject)
	assert.Nil(t, d.Email)
}

// TestUser_JSONMarshal_IdPFieldsNeverLeak verifies that json.Marshal of a User
// never includes Subject or Email, enforced by json:"-" tags. Guards against
// accidental direct serialization of User in a handler bypassing the DTO.
func TestUser_JSONMarshal_IdPFieldsNeverLeak(t *testing.T) {
	subVal := "idp-subject-MUST-NOT-APPEAR"
	emailVal := "secret@idp.example.com"
	u := User{
		ID:       uuid.New(),
		Subject:  &subVal,
		Email:    &emailVal,
		Username: "alice",
		Role:     identity.RoleAdmin,
	}
	b, err := json.Marshal(u)
	require.NoError(t, err)
	body := string(b)
	assert.NotContains(t, body, "MUST-NOT-APPEAR", "Subject value must not appear in JSON output")
	assert.NotContains(t, body, "secret@idp.example.com", "Email value must not appear in JSON output")
	assert.NotContains(t, body, "Subject", "Subject field name must not appear in JSON output")
	assert.NotContains(t, body, "Email", "Email field name must not appear in JSON output")
	assert.Contains(t, body, "alice", "Username must still appear")
}
