package dashboard_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/dashboard"
)

// The Go browser renderer (adapter/renderreview) serves Assets() to headless
// Chrome and navigates to /render.html; the embedded build must contain it
// and every asset it references.
func TestAssets_ContainsRenderSurface(t *testing.T) {
	assets := dashboard.Assets()
	for _, name := range []string{"index.html", "render.html"} {
		if _, err := fs.Stat(assets, name); err != nil {
			t.Fatalf("embedded dashboard is missing %s (run `make ui`): %v", name, err)
		}
	}
	raw, err := fs.ReadFile(assets, "render.html")
	if err != nil {
		t.Fatalf("reading render.html: %v", err)
	}
	if !regexp.MustCompile(`/assets/render-[A-Za-z0-9_-]+\.js`).Match(raw) {
		t.Fatalf("render.html does not load its built entry chunk:\n%s", raw)
	}
	for _, ref := range regexp.MustCompile(`/assets/[A-Za-z0-9_.-]+\.(?:js|css)`).FindAll(raw, -1) {
		if _, err := fs.Stat(assets, strings.TrimPrefix(string(ref), "/")); err != nil {
			t.Errorf("render.html references %s, which is not embedded: %v", ref, err)
		}
	}
}
