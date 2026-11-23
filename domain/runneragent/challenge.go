package runneragent

import "encoding/base64"

// ChallengeMessage is the exact byte string a runner signs with its machine
// key: "arbitron-runner-v1:" + keyID + ":" + base64(StdEncoding, nonce).
func ChallengeMessage(keyID string, nonce []byte) []byte {
	return []byte("arbitron-runner-v1:" + keyID + ":" + base64.StdEncoding.EncodeToString(nonce))
}
