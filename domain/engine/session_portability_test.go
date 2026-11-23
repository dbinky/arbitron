package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
)

func TestBundleChecksumAndVerify(t *testing.T) {
	data := []byte("session bytes")
	sum := sha256.Sum256(data)
	assert.Equal(t, hex.EncodeToString(sum[:]), engine.BundleChecksum(data))

	ok := engine.SessionBundle{Data: data, Checksum: engine.BundleChecksum(data)}
	assert.NoError(t, ok.Verify())

	bad := ok
	bad.Data = []byte("session bytez")
	assert.ErrorIs(t, bad.Verify(), engine.ErrBundleChecksum)
	assert.False(t, errors.Is(bad.Verify(), engine.ErrBundleRequiresRebuild), "checksum and rebuild are distinct sentinels")

	empty := engine.SessionBundle{}
	assert.ErrorIs(t, empty.Verify(), engine.ErrBundleChecksum, "a bundle without a checksum is never trusted")
}
