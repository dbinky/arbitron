# Stage 1: build the dashboard SPA. Doing this in-image means every
# `docker compose build` embeds a freshly-built UI from dashboard/ source —
# no dependence on the hand-maintained, easily-stale adapter/dashboard/dist
# checked into git (that copy exists only so a host `go build` compiles without
# a Node toolchain; refresh it with `make ui`).
# SUPPLY CHAIN: base images are pinned by digest (the human-readable tag is kept
# as a comment for readability). Refresh a digest with:
#   docker buildx imagetools inspect <image>:<tag>
FROM node:22-alpine@sha256:968df39aedcea65eeb078fb336ed7191baf48f972b4479711397108be0966920 AS dashboard-build
WORKDIR /app/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
# npm install (not ci): the committed lockfile is generated on the dev host
# (darwin) and omits the Linux-musl optional native bindings (rollup/vite,
# @emnapi/*) that Alpine needs, which `npm ci` rejects as out-of-sync.
RUN npm install --no-audit --no-fund
COPY dashboard/ ./
RUN npm run build

# Stage 2: build the Go binary, embedding the freshly-built dashboard.
FROM golang:1.25.5-alpine@sha256:ac09a5f469f307e5da71e766b0bd59c9c49ea460a528cc3e6686513d64a6f1fb AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed embed dir with the fresh build so the //go:embed dist/*
# in adapter/dashboard picks up the current UI rather than a stale checked-in copy.
RUN rm -rf adapter/dashboard/dist
COPY --from=dashboard-build /app/dashboard/dist ./adapter/dashboard/dist
RUN CGO_ENABLED=0 go build -o /arbitron ./cmd/arbitron

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
# bash + coreutils are required by the claude-cli Bash tool: the agent harness
# probes for a real POSIX shell and refuses to run with busybox-only `sh`
# ("No suitable shell found"). coreutils gives GNU date/timeout/etc. that the
# review-persona prompts and gh scripting expect.
# org-readiness Phase 2: BOTH agent backends ship in the image so either can be
# selected at runtime (claude-code | opencode). Both are installed via npm.
#
# SUPPLY CHAIN: apk packages and npm CLIs are pinned to exact versions so a
# rebuild is reproducible and cannot silently pull a new (possibly compromised)
# release. Refresh apk pins with `apk policy <pkg>` against the pinned base
# image; refresh npm pins with `npm view <pkg> version`.
#
# OPENCODE PIN (single source of truth — keep in sync with install.sh
# OPENCODE_NPM_PKG and install.ps1 OpencodeNpmPackage). Package: opencode-ai;
# runtime gate: >= 0.4.0 (adapter/opencode/version.go minOpencodeVersion).
RUN apk add --no-cache \
        ca-certificates=20260413-r0 \
        bash=5.2.26-r0 \
        coreutils=9.5-r2 \
        nodejs=20.15.1-r0 \
        npm=10.9.1-r0 \
        git=2.45.4-r0 \
        github-cli=2.47.0-r4 \
        make=4.4.1-r2 \
 && npm install -g @anthropic-ai/claude-code@2.1.170 \
 && npm install -g opencode-ai@1.17.1 \
 && addgroup -S arbitron \
 && adduser -S -h /home/arbitron -s /bin/sh -G arbitron arbitron \
 && mkdir -p /data /workspaces /claude-config /samples \
            /home/arbitron/.config/gh /home/arbitron/Repos /home/arbitron/go \
 && chown -R arbitron:arbitron /data /workspaces /claude-config /samples /home/arbitron
COPY --from=builder /arbitron /usr/local/bin/arbitron
# Bundled sample workflows, seeded into the shared scope on first boot.
COPY --chown=arbitron:arbitron samples/ /samples/
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
# Sample workflows are bundled in /samples and seeded into the shared scope on
# boot.  These defaults are explicit so the image self-seeds regardless of how
# it is run; operators may override either var to disable or redirect.
ENV ARBITRON_SAMPLES_SEED=true \
    ARBITRON_SAMPLES_SOURCEDIR=/samples
VOLUME /data
USER arbitron
EXPOSE 8080
ENTRYPOINT ["arbitron", "serve"]
