package dashboard_test

import (
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
	handler := dashboard.Handler()
	req := httptest.NewRequest("GET", "/assets/index-C3wlROOj.css", nil)
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
