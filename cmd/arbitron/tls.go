package main

import (
	"crypto/tls"
	"fmt"

	"golang.org/x/crypto/acme/autocert"

	"github.com/arbitron/arbitron/domain/config"
)

// buildTLSConfig translates the server TLS config into a *tls.Config and, when
// autocert is used, the *autocert.Manager whose HTTP-01 handler must be served
// on :80. Returns (nil, nil, nil) when TLS is disabled (plain HTTP). The config
// loader has already validated that exactly one mode is configured.
func buildTLSConfig(c config.TLSConfig) (*tls.Config, *autocert.Manager, error) {
	if !c.Enabled {
		return nil, nil, nil
	}
	if c.CertFile != "" && c.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("loading TLS keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, nil, nil
	}
	if c.AutocertDomain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(c.AutocertDomain),
			Cache:      autocert.DirCache(c.AutocertCacheDir),
		}
		return m.TLSConfig(), m, nil
	}
	return nil, nil, fmt.Errorf("tls enabled but neither cert/key nor autocert configured")
}
