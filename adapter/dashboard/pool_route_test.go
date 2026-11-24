package dashboard_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/dashboard"
)

var scriptRef = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+\.js)"`)

func serve(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return rec.Code, string(body)
}

// Deep links and the scripts loaded by the SPA must serve the wired Pool views.
func TestEmbeddedDist_ServesPoolRoute(t *testing.T) {
	h := dashboard.Handler()
	for _, path := range []string{"/pool", "/pool/runners/builtin"} {
		code, html := serve(t, h, path)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, code)
		}
		if !strings.Contains(html, `<div id="root"></div>`) {
			t.Fatalf("GET %s did not fall back to the SPA index.html", path)
		}
	}
	_, html := serve(t, h, "/pool")
	refs := scriptRef.FindAllStringSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatal("index.html references no JavaScript assets")
	}
	var bundle strings.Builder
	for _, ref := range refs {
		code, js := serve(t, h, ref[1])
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", ref[1], code)
		}
		bundle.WriteString(js)
	}
	for _, marker := range []string{
		"arbitron-pool-view", "arbitron-runner-detail", "/pool/runners/:id", "/api/v1/runners", "Add runner",
	} {
		if !strings.Contains(bundle.String(), marker) {
			t.Errorf("embedded dashboard does not serve the Pool route: %q not found (run `make ui` and commit adapter/dashboard/dist)", marker)
		}
	}
}
