// Package identity stores the runner's ed25519 machine key (PKCS#8 PEM, 0600).
package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileStore implements runneragent.IdentityStore.
type FileStore struct {
	path string
	mu   sync.Mutex
	key  ed25519.PrivateKey
}

// New returns a store for the key at path.
func New(path string) *FileStore { return &FileStore{path: path} }

// Key loads the machine key, creating it on first use.
func (s *FileStore) Key(ctx context.Context) (ed25519.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	k, err := s.load()
	if errors.Is(err, fs.ErrNotExist) {
		k, err = s.create()
		if errors.Is(err, fs.ErrExist) { // another process created it first
			k, err = s.load()
		}
	}
	if err != nil {
		return nil, err
	}
	s.key = k
	return k, nil
}

func (s *FileStore) load() (ed25519.PrivateKey, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("identity: %s has mode %04o; the machine key must be 0600", s.path, perm)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		if len(b) == 0 { // a concurrent creator has not written yet
			return nil, fmt.Errorf("identity: %s is empty", s.path)
		}
		return nil, fmt.Errorf("identity: %s is not a PEM private key", s.path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("identity: %s: %w", s.path, err)
	}
	k, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("identity: %s is not an ed25519 key", s.path)
	}
	return k, nil
}

func (s *FileStore) create() (ed25519.PrivateKey, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("identity: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".machine_ed25519-*")
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// os.Link fails with ErrExist when another process won the race, so the
	// published key is complete and never overwritten.
	if err := os.Link(tmp.Name(), s.path); err != nil {
		return nil, err
	}
	return priv, nil
}
