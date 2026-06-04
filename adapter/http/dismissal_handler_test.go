package http_test

import (
	"context"
	"net/http/httptest"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUserRepo implements instance.UserRepository.
type mockUserRepo struct{ id uuid.UUID }

func (m *mockUserRepo) GetDefaultUser(_ context.Context) (uuid.UUID, error) { return m.id, nil }

func TestCurrentUser_ResolvesDefaultUser(t *testing.T) {
	want := uuid.New()
	resolver := adapthttp.NewCurrentUserResolver(&mockUserRepo{id: want})
	r := httptest.NewRequest("GET", "/", nil)

	got, err := resolver.CurrentUser(r)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
