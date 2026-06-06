package main

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/config"
)

// listenAddr binds an ephemeral port, closes the listener, and returns the
// "127.0.0.1:<port>" address so a server can claim it. There is a tiny TOCTOU
// window but it is acceptable for a test on loopback.
func listenAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// TestServeHTTP_CertKeyTLSRoundTrip is the load-bearing integration guard: it
// stands up the real serve loop with a self-signed cert on an ephemeral port,
// performs a TLS round trip via http.Client asserting 200, then signals
// shutdown and asserts the listener actually closed (a follow-up request fails).
func TestServeHTTP_CertKeyTLSRoundTrip(t *testing.T) {
	cert, key := genSelfSigned(t)
	tlsConf, challengeSrv, err := buildTLSConfig(config.TLSConfig{
		Enabled: true, CertFile: cert, KeyFile: key,
	}, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, tlsConf)
	assert.Nil(t, challengeSrv) // cert/key mode has no ACME challenge server

	addr := listenAddr(t)
	srv := &http.Server{
		Addr:      addr,
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		TLSConfig: tlsConf,
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- serveHTTP(context.Background(), slog.Default(), srv, nil, stop) }()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	url := "https://" + addr + "/"
	resp := getWithRetry(t, client, url)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	close(stop)
	require.NoError(t, <-done)

	// The listener must be closed after shutdown.
	_, err = client.Get(url)
	require.Error(t, err)
}

// TestServeHTTP_PlainHTTPFallback asserts the disabled-TLS path still serves
// plain HTTP and shuts down cleanly.
func TestServeHTTP_PlainHTTPFallback(t *testing.T) {
	tlsConf, challengeSrv, err := buildTLSConfig(config.TLSConfig{Enabled: false}, slog.Default())
	require.NoError(t, err)
	assert.Nil(t, tlsConf)
	assert.Nil(t, challengeSrv)

	addr := listenAddr(t)
	srv := &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- serveHTTP(context.Background(), slog.Default(), srv, nil, stop) }()

	client := &http.Client{Timeout: 2 * time.Second}
	url := "http://" + addr + "/"
	resp := getWithRetry(t, client, url)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	close(stop)
	require.NoError(t, <-done)

	_, err = client.Get(url)
	require.Error(t, err)
}

// getWithRetry polls the server until it accepts connections (the goroutine may
// not have bound yet) without sleeping on a fixed duration.
func getWithRetry(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became reachable: %v", err)
		}
	}
}
