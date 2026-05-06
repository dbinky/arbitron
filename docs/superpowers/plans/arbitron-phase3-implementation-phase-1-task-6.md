# Phase 3.1 Task 6: Go Embed & Static File Serving

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed the built dashboard into the Go binary so `arbitron serve` serves both the API and the SPA from a single process.

**Architecture:** A new `adapter/dashboard/` package uses `//go:embed` to bundle `dashboard/dist/`. The chi router serves embedded static files as a catch-all after API routes. SPA fallback serves `index.html` for any non-file path.

**Tech Stack:** Go embed, chi, net/http

---

### Task 6.1: Build the dashboard and create the embed package

**Files:**
- Create: `adapter/dashboard/embed.go`
- Create: `adapter/dashboard/embed_test.go`
- Create: `dashboard/dist/.gitkeep` (placeholder so embed works before first build)

- [ ] **Step 1: Build the dashboard**

```bash
cd dashboard && npm run build
```

Expected: `dashboard/dist/` is created with `index.html`, JS, and CSS files.

- [ ] **Step 2: Create the embed package**

Create `adapter/dashboard/embed.go`:

```go
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/*
var distFS embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("failed to create sub filesystem: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Try to serve the file directly.
		if path != "/" && !strings.HasSuffix(path, "/") {
			if f, err := sub.Open(strings.TrimPrefix(path, "/")); err == nil {
				f.Close()
				// Set cache headers for hashed assets.
				if strings.Contains(path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// SPA fallback: serve index.html for any path that doesn't match a file.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 3: Create a placeholder so embed works without a build**

```bash
mkdir -p adapter/dashboard/dist
echo '<!DOCTYPE html><html><body>Build the dashboard first: cd dashboard && npm run build</body></html>' > adapter/dashboard/dist/index.html
```

Note: The actual embed uses a symlink or copy step. For the Go embed to work, the `dist/` directory must be relative to the Go package. Two approaches:
- **Option A (symlink):** `cd adapter/dashboard && ln -s ../../dashboard/dist dist`
- **Option B (copy in build):** A build script copies `dashboard/dist/` to `adapter/dashboard/dist/`

Use **Option B** — add a build step. Create a simple build script or use a Makefile target. For now, manually copy after building:

```bash
rm -rf adapter/dashboard/dist
cp -r dashboard/dist adapter/dashboard/dist
```

- [ ] **Step 4: Write a test**

Create `adapter/dashboard/embed_test.go`:

```go
package dashboard_test

import (
	"net/http"
	"net/http/httptest"
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

	body := rec.Body.String()
	if len(body) == 0 {
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
```

- [ ] **Step 5: Run test**

```bash
go test ./adapter/dashboard/ -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add adapter/dashboard/
git commit -m "feat: add Go embed package for serving dashboard SPA"
```

---

### Task 6.2: Wire dashboard handler into the server

**Files:**
- Modify: `adapter/http/server.go`
- Modify: `cmd/arbitron/main.go`

- [ ] **Step 1: Add dashboard catch-all route in server.go**

In `adapter/http/server.go`, add a parameter to `NewRouter` for the dashboard handler:

Add a new parameter `dashboardHandler http.Handler` at the end of the `NewRouter` signature (or use variadic like webhookHandler).

After all `/api/v1` routes are registered, add:

```go
if dashboardHandler != nil {
	r.NotFound(dashboardHandler.ServeHTTP)
}
```

This makes the dashboard the catch-all for non-API routes.

- [ ] **Step 2: Pass dashboard handler from main.go**

In `cmd/arbitron/main.go`, import the dashboard package:

```go
"github.com/arbitron/arbitron/adapter/dashboard"
```

Create the handler and pass it to `NewRouter`:

```go
dashHandler := dashboard.Handler()
```

Pass `dashHandler` as the new parameter to `NewRouter`.

- [ ] **Step 3: Build and test end-to-end**

```bash
cd dashboard && npm run build
rm -rf ../adapter/dashboard/dist && cp -r dist ../adapter/dashboard/dist
cd .. && go build -o arbitron ./cmd/arbitron/
./arbitron serve
```

Open `http://localhost:8080` — should show the dashboard Queue View.
Open `http://localhost:8080/api/v1/health` — should return `{"status":"ok"}`.
Open `http://localhost:8080/instances/any-id` — should show the SPA (execution view placeholder).

- [ ] **Step 4: Commit**

```bash
git add adapter/http/server.go cmd/arbitron/main.go
git commit -m "feat: wire dashboard embed into server as catch-all route"
```
