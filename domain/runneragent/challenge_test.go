package runneragent_test

import (
	"crypto/ed25519"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChallengeMessageFormat(t *testing.T) {
	nonce := []byte{0xff, 0x00, 0x10}
	got := runneragent.ChallengeMessage("2f0c7a2e-1111-4222-8333-944455556666", nonce)
	assert.Equal(t, "arbitron-runner-v1:2f0c7a2e-1111-4222-8333-944455556666:/wAQ", string(got))
}
func TestChallengeMessageSignatureBindsKeyAndNonce(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 1
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, runneragent.ChallengeMessage("k1", []byte("nonce-a")))
	require.True(t, ed25519.Verify(pub, runneragent.ChallengeMessage("k1", []byte("nonce-a")), sig))
	assert.False(t, ed25519.Verify(pub, runneragent.ChallengeMessage("k2", []byte("nonce-a")), sig))
	assert.False(t, ed25519.Verify(pub, runneragent.ChallengeMessage("k1", []byte("nonce-b")), sig))
}
