.PHONY: ui build test test-ui test-all

# Rebuild the dashboard and sync it into the embedded dir that the Go binary
# serves (adapter/dashboard/dist, via //go:embed dist/*). Run this after
# changing anything under dashboard/ so a host `go build`/`go run` serves the
# current UI. The Docker image builds the dashboard itself, so this is only for
# local (non-Docker) runs — but commit the refreshed adapter/dashboard/dist so
# the checked-in artifact does not drift.
ui:
	cd dashboard && npm ci && npm run build
	rm -rf adapter/dashboard/dist
	cp -r dashboard/dist adapter/dashboard/dist

# Build the server binary (embeds whatever is in adapter/dashboard/dist).
build:
	CGO_ENABLED=0 go build -o arbitron ./cmd/arbitron

# Run the Go test suite (the project's primary gate). Go-only — it does NOT
# cover the dashboard (Phase 3) surface; use `test-all` for the full gate.
test:
	go test ./...

# Run the dashboard test suite: TypeScript type-check + Vitest. Not part of
# `test`; kept separate because it needs Node, not Go.
test-ui:
	cd dashboard && npx tsc --noEmit && npx vitest run

# Full Definition-of-Done gate: the Go suite AND the dashboard suite. "Full
# test suite green" means this target passes, not just `test`.
test-all: test test-ui
