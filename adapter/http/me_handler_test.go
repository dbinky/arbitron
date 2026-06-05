package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

func TestMeHandler_ReturnsProjection(t *testing.T) {
	uid := uuid.New()
	id := identity.Identity{
		UserID:         uid,
		Username:       "alice",
		Role:           identity.RoleUser,
		Departments:    []string{"engineering", "platform"},
		LedDepartments: []string{"platform"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))

	NewMeHandler().Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		UserID         string   `json:"userId"`
		Username       string   `json:"username"`
		Role           string   `json:"role"`
		Departments    []string `json:"departments"`
		LedDepartments []string `json:"ledDepartments"`
		IsAdmin        bool     `json:"isAdmin"`
		Subject        string   `json:"subject"` // must remain empty (never serialized)
		Email          string   `json:"email"`   // must remain empty (never serialized)
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != uid.String() {
		t.Errorf("userId = %q, want %q", got.UserID, uid.String())
	}
	if got.Username != "alice" {
		t.Errorf("username = %q, want alice", got.Username)
	}
	if got.Role != "user" {
		t.Errorf("role = %q, want user", got.Role)
	}
	if !reflect.DeepEqual(got.Departments, []string{"engineering", "platform"}) {
		t.Errorf("departments = %v, want [engineering platform]", got.Departments)
	}
	if !reflect.DeepEqual(got.LedDepartments, []string{"platform"}) {
		t.Errorf("ledDepartments = %v, want [platform]", got.LedDepartments)
	}
	if got.IsAdmin {
		t.Error("isAdmin = true, want false for a user role")
	}
	if got.Subject != "" || got.Email != "" {
		t.Errorf("leaked subject/email: subject=%q email=%q", got.Subject, got.Email)
	}
}

func TestMeHandler_AdminFlag(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin, Departments: []string{"default"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))

	NewMeHandler().Me(rec, req)

	var got struct {
		IsAdmin bool   `json:"isAdmin"`
		Role    string `json:"role"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if !got.IsAdmin || got.Role != "admin" {
		t.Errorf("isAdmin=%v role=%q, want true/admin", got.IsAdmin, got.Role)
	}
}

func TestMeHandler_EmptyDepartmentsSerializeAsEmptyArray(t *testing.T) {
	// departments/ledDepartments must serialize as [] (not null) so clients can
	// rely on an array shape.
	id := identity.Identity{UserID: uuid.New(), Username: "x", Role: identity.RoleUser}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))

	NewMeHandler().Me(rec, req)

	body := rec.Body.String()
	if !meContains(body, `"departments":[]`) || !meContains(body, `"ledDepartments":[]`) {
		t.Errorf("empty arrays not serialized as []: %s", body)
	}
}

func TestMeHandler_NoIdentityReturns401(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil) // no identity in context
	NewMeHandler().Me(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity", rec.Code)
	}
}

// TestMeHandler_NoPersonalIdentifiersInJSON checks the raw JSON response for
// PII key patterns. The struct-decode check in TestMeHandler_ReturnsProjection
// catches known field names, but this test verifies the actual serialized JSON
// does not carry any unexpected identifier-style key. It catches a future
// developer adding an emailFwd or sub field to meView without updating tests.
func TestMeHandler_NoPersonalIdentifiersInJSON(t *testing.T) {
	id := identity.Identity{
		UserID:      uuid.New(),
		Username:    "alice",
		Role:        identity.RoleUser,
		Departments: []string{"engineering"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))
	NewMeHandler().Me(rec, req)

	body := rec.Body.String()
	piiKeys := []string{`"subject"`, `"email"`, `"sub"`, `"emailVerified"`, `"emailAddress"`}
	for _, key := range piiKeys {
		if meContains(body, key) {
			t.Errorf("JSON response contains PII key %s: %s", key, body)
		}
	}
}

// TestMeHandler_AllowedKeysOnly verifies that the JSON response contains
// exactly the expected set of top-level keys and no extras. A new field
// silently added to meView (e.g. for debugging) would be caught here.
func TestMeHandler_AllowedKeysOnly(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Username: "bob", Role: identity.RoleAdmin, Departments: []string{"eng"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))
	NewMeHandler().Me(rec, req)

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"userId":         true,
		"username":       true,
		"role":           true,
		"departments":    true,
		"ledDepartments": true,
		"isAdmin":        true,
	}
	for key := range rawMap {
		if !allowed[key] {
			t.Errorf("unexpected key %q in /me response — ensure no PII or internal fields are leaked", key)
		}
	}
	// All allowed keys must be present (no fields silently dropped).
	for key := range allowed {
		if _, ok := rawMap[key]; !ok {
			t.Errorf("expected key %q missing from /me response", key)
		}
	}
}

// TestMeHandler_LocalModeIdentity verifies that the local-mode identity shape
// (admin, single default-group department, nil LedDepartments) serializes
// correctly. This is the inertness check for FA #7: in local mode the /me
// response must be valid and usable by the client, not null or empty.
func TestMeHandler_LocalModeIdentity(t *testing.T) {
	uid := uuid.New()
	// Mirrors what AuthMiddleware injects for local mode.
	id := identity.Identity{
		UserID:         uid,
		Username:       "local",
		Role:           identity.RoleAdmin,
		Departments:    []string{"default"},
		LedDepartments: nil, // local mode never sets LedDepartments
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))
	NewMeHandler().Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		UserID         string   `json:"userId"`
		Role           string   `json:"role"`
		Departments    []string `json:"departments"`
		LedDepartments []string `json:"ledDepartments"`
		IsAdmin        bool     `json:"isAdmin"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Role != "admin" || !got.IsAdmin {
		t.Errorf("role=%q isAdmin=%v, want admin/true", got.Role, got.IsAdmin)
	}
	if !reflect.DeepEqual(got.Departments, []string{"default"}) {
		t.Errorf("Departments = %v, want [default]", got.Departments)
	}
	// nil LedDepartments must be serialized as [] (not null) in local mode.
	if got.LedDepartments == nil {
		t.Error("LedDepartments serialized as null, want []")
	}
	if len(got.LedDepartments) != 0 {
		t.Errorf("LedDepartments = %v, want empty in local mode", got.LedDepartments)
	}
}

// meContains is a tiny local helper (avoid importing strings just for this).
func meContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
