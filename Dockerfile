# Stage 1: build the dashboard SPA. Doing this in-image means every
# `docker compose build` embeds a freshly-built UI from dashboard/ source —
# no dependence on the hand-maintained, easily-stale adapter/dashboard/dist
# checked into git (that copy exists only so a host `go build` compiles without
# a Node toolchain; refresh it with `make ui`).
FROM node:22-alpine AS dashboard-build
WORKDIR /app/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
# npm install (not ci): the committed lockfile is generated on the dev host
# (darwin) and omits the Linux-musl optional native bindings (rollup/vite,
# @emnapi/*) that Alpine needs, which `npm ci` rejects as out-of-sync.
RUN npm install --no-audit --no-fund
COPY dashboard/ ./
RUN npm run build

# Stage 2: build the Go binary, embedding the freshly-built dashboard.
FROM golang:1.25.5-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed embed dir with the fresh build so the //go:embed dist/*
# in adapter/dashboard picks up the current UI rather than a stale checked-in copy.
RUN rm -rf adapter/dashboard/dist
COPY --from=dashboard-build /app/dashboard/dist ./adapter/dashboard/dist
RUN CGO_ENABLED=0 go build -o /arbitron ./cmd/arbitron

FROM alpine:3.20
# bash + coreutils are required by the claude-cli Bash tool: the agent harness
# probes for a real POSIX shell and refuses to run with busybox-only `sh`
# ("No suitable shell found"). coreutils gives GNU date/timeout/etc. that the
# review-persona prompts and gh scripting expect.
# org-readiness Phase 2: BOTH agent backends ship in the image so either can be
# selected at runtime (claude-code | opencode). Both are installed via npm.
#
# OPENCODE PIN (single source of truth — keep in sync with install.sh
# OPENCODE_NPM_PKG and install.ps1 OpencodeNpmPackage). Append @<version> to pin
# an exact release once confirmed; left unpinned because the exact published
# version is environment-dependent. NOTE(opencode-binary): verify the package
# name/version against the installed modern binary (adapter/opencode/args.go).
RUN apk add --no-cache ca-certificates bash coreutils nodejs npm git github-cli make \
 && npm install -g @anthropic-ai/claude-code \
 && npm install -g opencode-ai \
 && addgroup -S arbitron \
 && adduser -S -h /home/arbitron -s /bin/sh -G arbitron arbitron \
 && mkdir -p /data /workspaces /claude-config \
            /home/arbitron/.config/gh /home/arbitron/Repos /home/arbitron/go \
 && chown -R arbitron:arbitron /data /workspaces /claude-config /home/arbitron
COPY --from=builder /arbitron /usr/local/bin/arbitron
# Copy the same Go toolchain the builder used so senior-engineer node can
# run `go build`, `go test`, `go vet` to verify Go-project fixes. For
# polyglot use, add more toolchains (node already present; add python/cargo
# etc. as needed).
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/home/arbitron/go/bin:$PATH
ENV GOPATH=/home/arbitron/go
# SHELL is on the claude-cli env allowlist; set it so the agent harness selects
# bash deterministically rather than probing.
ENV SHELL=/bin/bash
VOLUME /data
USER arbitron
EXPOSE 8080
ENTRYPOINT ["arbitron", "serve"]
