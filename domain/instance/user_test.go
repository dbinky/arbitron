package instance

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
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
