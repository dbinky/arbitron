package dashboard_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/dashboard"
)

func TestHandler_ServesIndexHTML(t *testing.T) {
	handler := dashboard.Handler()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty body")
	}
}

func TestHandler_SPAFallback(t *testing.T) {
	handler := dashboard.Handler()
	req := httptest.NewRequest("GET", "/instances/some-uuid", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandler_ServesStaticAsset(t *testing.T) {
	// Discover the actual CSS asset name at test time — Vite content-hashes
	// filenames on every build, so hardcoding the name would break on rebuild.
	entries, err := fs.ReadDir(dashboard.AssetsForTest(), "assets")
	if err != nil {
		t.Fatalf("reading assets dir: %v", err)
	}
	var cssPath string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".css") {
			cssPath = "/assets/" + e.Name()
			break
		}
	}
	if cssPath == "" {
		t.Skip("no CSS asset found in embedded filesystem")
	}

	handler := dashboard.Handler()
	req := httptest.NewRequest("GET", cssPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Errorf("expected immutable cache-control header, got %q", cc)
	}
}
