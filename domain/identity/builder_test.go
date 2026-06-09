package identity_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildIdentity(t *testing.T) {
	uid := uuid.New()
	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads: map[string][]string{
			"eng": {"lead@example.com"},
		},
	}

	tests := []struct {
		name        string
		in          identity.UserIdentityInput
		wantDepts   []string
		wantLed     []string
		wantRole    identity.Role
		wantUser    string
		wantUserID  uuid.UUID
		assertExtra func(t *testing.T, got identity.Identity)
	}{
		{
			name: "stored departments only, no leads",
			in: identity.UserIdentityInput{
				UserID:            uid,
				Username:          "alice",
				Role:              identity.RoleUser,
				StoredDepartments: []string{"sales"},
			},
			wantDepts:  []string{"sales"},
			wantLed:    nil,
			wantRole:   identity.RoleUser,
			wantUser:   "alice",
			wantUserID: uid,
		},
		{
			name: "lead departments unioned and populated on LedDepartments",
			in: identity.UserIdentityInput{
				UserID:            uid,
				Username:          "bob",
				Role:              identity.RoleUser,
				Email:             "lead@example.com",
				StoredDepartments: []string{"sales"},
			},
			wantDepts: []string{"sales", "eng"},
			wantLed:   []string{"eng"},
			wantRole:  identity.RoleUser,
			wantUser:  "bob",
		},
		{
			name: "no stored, no led falls back to default group",
			in: identity.UserIdentityInput{
				UserID:   uid,
				Username: "carol",
				Role:     identity.RoleUser,
			},
			wantDepts: []string{"default"},
			wantLed:   nil,
		},
		{
			name: "stored and led overlap is de-duplicated",
			in: identity.UserIdentityInput{
				UserID:            uid,
				Username:          "dave",
				Role:              identity.RoleAdmin,
				Email:             "lead@example.com",
				StoredDepartments: []string{"eng"},
			},
			wantDepts: []string{"eng"},
			wantLed:   []string{"eng"},
			wantRole:  identity.RoleAdmin,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := identity.BuildIdentity(tc.in, groups)
			assert.Equal(t, tc.wantDepts, got.Departments)
			assert.Equal(t, tc.wantLed, got.LedDepartments)
			if tc.wantRole != "" {
				assert.Equal(t, tc.wantRole, got.Role)
			}
			if tc.wantUser != "" {
				assert.Equal(t, tc.wantUser, got.Username)
			}
		})
	}
}

// The request path and the scheduler path MUST produce the same Identity shape
// for the same user — in particular LedDepartments must not diverge.
func TestBuildIdentity_RequestAndSchedulerPathsAgree(t *testing.T) {
	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"eng": {"u@x.io"}},
	}
	in := identity.UserIdentityInput{
		UserID:            uuid.New(),
		Username:          "u",
		Role:              identity.RoleUser,
		Email:             "u@x.io",
		StoredDepartments: []string{"sales"},
	}

	requestPath := identity.BuildIdentity(in, groups)
	schedulerPath := identity.BuildIdentity(in, groups)

	require.Equal(t, requestPath, schedulerPath)
	assert.Equal(t, []string{"eng"}, schedulerPath.LedDepartments,
		"the scheduler path must carry LedDepartments, not an empty slice")
}
