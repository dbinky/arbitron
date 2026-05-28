FROM golang:1.25.5-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /arbitron ./cmd/arbitron

FROM alpine:3.20
# bash + coreutils are required by the claude-cli Bash tool: the agent harness
# probes for a real POSIX shell and refuses to run with busybox-only `sh`
# ("No suitable shell found"). coreutils gives GNU date/timeout/etc. that the
# review-persona prompts and gh scripting expect.
RUN apk add --no-cache ca-certificates bash coreutils nodejs npm git github-cli make \
 && npm install -g @anthropic-ai/claude-code \
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
