FROM golang:1.25.0-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /arbitron ./cmd/arbitron

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
RUN addgroup -S arbitron && adduser -S arbitron -G arbitron
COPY --from=builder /arbitron /usr/local/bin/arbitron
RUN mkdir -p /data /workspaces && chown arbitron:arbitron /data /workspaces
VOLUME /data
USER arbitron
EXPOSE 8080
ENTRYPOINT ["arbitron", "serve"]
