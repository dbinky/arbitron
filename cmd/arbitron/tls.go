package main

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"

	"golang.org/x/crypto/acme/autocert"

	"github.com/arbitron/arbitron/domain/config"
)

// buildTLSConfig translates the server TLS config into a *tls.Config and, when
// autocert is used, the ACME HTTP-01 challenge *http.Server that must be served
// on :80 (nil otherwise). Returns (nil, nil, nil) when TLS is disabled (plain
// HTTP). The config loader has already validated that exactly one mode is
// configured, that autocert runs on :443, and that autocert has a cache dir.
//
// logger is used for non-fatal hygiene warnings (a group-/world-readable private
// key, a loose pre-existing autocert cache dir).
func buildTLSConfig(c config.TLSConfig, logger *slog.Logger) (*tls.Config, *http.Server, error) {
	if !c.Enabled {
		return nil, nil, nil
	}
	if c.CertFile != "" && c.KeyFile != "" {
		warnIfKeyReadable(logger, c.KeyFile)
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("loading TLS keypair: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil, nil
	}
	if c.AutocertDomain != "" {
		warnIfCacheDirReadable(logger, c.AutocertCacheDir)
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(c.AutocertDomain),
			Cache:      autocert.DirCache(c.AutocertCacheDir),
		}
		// The ACME HTTP-01 challenge must be reachable on :80. Built at the same
		// boundary as the autocert.Manager so the caller owns a single, named
		// challenge server lifecycle (started and shut down alongside the main
		// listener) rather than inlining a goroutine.
		challengeSrv := &http.Server{Addr: ":80", Handler: m.HTTPHandler(nil)}
		return m.TLSConfig(), challengeSrv, nil
	}
	// unreachable when loader validation ran: validateTLS rejects "enabled but
	// neither cert/key nor autocert". Kept as a soft error (no panic) so a
	// direct caller that bypasses the loader still gets a clear failure.
	return nil, nil, fmt.Errorf("tls enabled but neither cert/key nor autocert configured")
}

// warnIfKeyReadable warns (does not fail) when the TLS private key file is
// group- or world-readable. A non-fatal warning (not a hard error) avoids
// breaking legitimate root-owned 0640 group-readable setups while still
// surfacing the common 0644/0666 mistake. No-op on Windows, whose file mode
// bits do not map to POSIX rwx permissions.
func warnIfKeyReadable(logger *slog.Logger, keyFile string) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		return // LoadX509KeyPair will report a missing/unreadable key.
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		logger.Warn("TLS private key is group- or world-readable; restrict it to 0600",
			"keyfile", keyFile, "perm", fmt.Sprintf("%04o", perm))
	}
}

// warnIfCacheDirReadable warns (does not fail) when a pre-existing autocert
// cache directory is group- or world-accessible. autocert.DirCache creates a
// missing dir with 0700 and writes key material 0600, but it reuses an existing
// dir without tightening it — so a pre-created loose dir (e.g. world-readable
// /tmp/acme) would leak cached Let's Encrypt key material. No-op on Windows.
func warnIfCacheDirReadable(logger *slog.Logger, cacheDir string) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(cacheDir)
	if err != nil {
		return // missing dir is fine; DirCache creates it 0700.
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		logger.Warn("autocert cache dir is group- or world-accessible; cached private keys may leak — restrict it to 0700",
			"dir", cacheDir, "perm", fmt.Sprintf("%04o", perm))
	}
}
