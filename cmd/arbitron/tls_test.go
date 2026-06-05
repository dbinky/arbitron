package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/config"
)

// genSelfSigned writes a throwaway self-signed cert/key pair to temp files.
func genSelfSigned(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31-1, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, certPEM.Close())

	keyBytes, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM, err := os.Create(keyPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	require.NoError(t, keyPEM.Close())

	return certPath, keyPath
}

func TestBuildTLSConfig_Disabled(t *testing.T) {
	tc, mgr, err := buildTLSConfig(config.TLSConfig{Enabled: false})
	require.NoError(t, err)
	assert.Nil(t, tc)
	assert.Nil(t, mgr)
}

func TestBuildTLSConfig_CertKey(t *testing.T) {
	cert, key := genSelfSigned(t)
	tc, mgr, err := buildTLSConfig(config.TLSConfig{Enabled: true, CertFile: cert, KeyFile: key})
	require.NoError(t, err)
	require.NotNil(t, tc)
	assert.Len(t, tc.Certificates, 1)
	assert.Nil(t, mgr)
}

func TestBuildTLSConfig_Autocert(t *testing.T) {
	tc, mgr, err := buildTLSConfig(config.TLSConfig{
		Enabled:          true,
		AutocertDomain:   "a.example",
		AutocertCacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, tc)
	require.NotNil(t, mgr)
}

func TestBuildTLSConfig_BadKeypair(t *testing.T) {
	tc, mgr, err := buildTLSConfig(config.TLSConfig{
		Enabled:  true,
		CertFile: "/nope/cert.pem",
		KeyFile:  "/nope/key.pem",
	})
	require.Error(t, err)
	assert.Nil(t, tc)
	assert.Nil(t, mgr)
}
