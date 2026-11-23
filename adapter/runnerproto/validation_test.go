package runnerproto

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDecodePayload_RejectsNull(t *testing.T) {
	require.ErrorIs(t, DecodePayload(Envelope{V: 1, Kind: KindAck, Payload: json.RawMessage(`null`)}, &Ack{}), ErrMalformed)
}
