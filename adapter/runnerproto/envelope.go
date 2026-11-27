// adapter/runnerproto/envelope.go

// Package runnerproto is the versioned wire protocol shared by the Arbitron
// server's runner endpoint (adapter/http/runner_connect.go) and the
// arbitron-runner client (adapter/runneragent/wsclient). It is transport
// independent: every message is one JSON Envelope, idempotent by
// (runner, jobId, generation, seq), so a long-poll adapter could carry the
// same frames later without changing this package.
//
// Handshake on one WebSocket (one text message per envelope):
//
//  1. runner dials GET /api/v1/runners/connect with "Authorization: Runner ark_..."
//     (never a query parameter) plus any configured extra headers.
//  2. server  -> challenge          {keyId, nonce}            (nonce: 32 random bytes, base64 std)
//  3. runner  -> challenge_response {publicKey, signature}    (ed25519 over runner.ChallengeMessage(keyID, nonce))
//  4. runner  -> hello              {os, protocolVersion, runnerVersion, ..., sources, held}
//  5. server  -> admitted           {runnerId, heartbeatSeconds, protocolVersion}
//     or server -> refused          {code, reason, protocolMin, protocolMax} and the socket closes.
//  6. steady state: runner -> heartbeat (every heartbeatSeconds), accept, reject, output, result;
//     server -> dispatch, cancel, ack, fenced.
//
// Output frames are numbered per (jobId, generation) starting at 1; the server
// acks the highest in-order seq it accepted and, after a reconnect, acks the
// resume point for every adopted held job so the runner resends from there.
package runnerproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ProtocolVersion is the only execution protocol version this build speaks.
const ProtocolVersion = 1

// MaxFrameBytes caps one encoded envelope (1 MiB). Larger frames are refused
// on both encode and decode; bulk data uses the Phase 3 transfer endpoints.
const MaxFrameBytes = 1 << 20

// Message kinds.
const (
	KindDeleteCopy        = "delete_copy"
	KindHello             = "hello"
	KindHeartbeat         = "heartbeat"
	KindAccept            = "accept"
	KindReject            = "reject"
	KindOutput            = "output"
	KindResult            = "result"
	KindDispatch          = "dispatch"
	KindCancel            = "cancel"
	KindProbe             = "probe"
	KindProbeResult       = "probe_result"
	KindAck               = "ack"
	KindFenced            = "fenced"
	KindChallenge         = "challenge"
	KindChallengeResponse = "challenge_response"
	KindAdmitted          = "admitted"
	KindRefused           = "refused"
)

var (
	// ErrMalformed wraps every structural or payload validation failure.
	ErrMalformed = errors.New("runnerproto: malformed frame")
	// ErrFrameTooLarge rejects envelopes above MaxFrameBytes.
	ErrFrameTooLarge = errors.New("runnerproto: frame exceeds 1 MiB")
	// ErrUnsupportedVersion rejects a newer-version frame that is not part of
	// the minimal admission envelope (hello/refused).
	ErrUnsupportedVersion = errors.New("runnerproto: unsupported protocol version")
	// ErrUnknownKind rejects a current-version frame with an unknown kind.
	ErrUnknownKind = errors.New("runnerproto: unknown message kind")
)

// Envelope is one protocol frame.
type Envelope struct {
	V          int             `json:"v"`
	Kind       string          `json:"kind"`
	Seq        uint64          `json:"seq,omitempty"`
	JobID      string          `json:"jobId,omitempty"`
	Generation int64           `json:"generation,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

var knownKinds = map[string]bool{
	KindDeleteCopy:    true,
	KindPullWorkspace: true, KindPullReceipt: true, KindPullReceiptAck: true,
	KindSessionImport: true,
	KindHello:         true, KindHeartbeat: true, KindAccept: true, KindReject: true,
	KindOutput: true, KindResult: true, KindDispatch: true, KindCancel: true,
	KindProbe: true, KindProbeResult: true, KindAck: true, KindFenced: true,
	KindChallenge: true, KindChallengeResponse: true, KindAdmitted: true, KindRefused: true,
}

// jobScoped kinds must carry a UUID jobId and a generation >= 1.
var jobScoped = map[string]bool{
	KindSessionImport: true,
	KindAccept:        true, KindReject: true, KindOutput: true, KindResult: true,
	KindDispatch: true, KindCancel: true, KindAck: true, KindFenced: true,
}

// admissionKinds form the minimal envelope that stays readable across versions.
var admissionKinds = map[string]bool{KindHello: true, KindRefused: true}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

func validateEnvelope(e Envelope) error {
	if e.V < 1 {
		return malformed("missing or invalid version")
	}
	if e.Kind == "" {
		return malformed("missing kind")
	}
	if e.V > ProtocolVersion {
		if admissionKinds[e.Kind] {
			return nil
		}
		return fmt.Errorf("%w: v%d frame of kind %q", ErrUnsupportedVersion, e.V, e.Kind)
	}
	if !knownKinds[e.Kind] {
		return fmt.Errorf("%w: %q", ErrUnknownKind, e.Kind)
	}
	if jobScoped[e.Kind] {
		if _, err := uuid.Parse(e.JobID); err != nil {
			return malformed("%s frame needs a UUID jobId", e.Kind)
		}
		if e.Generation < 1 {
			return malformed("%s frame needs generation >= 1", e.Kind)
		}
	}
	if e.Kind == KindOutput && e.Seq < 1 {
		return malformed("output frame needs seq >= 1")
	}
	return nil
}

// Encode validates and serializes an envelope. A zero V is set to ProtocolVersion.
func Encode(e Envelope) ([]byte, error) {
	if e.V == 0 {
		e.V = ProtocolVersion
	}
	if err := validateEnvelope(e); err != nil {
		return nil, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, malformed("encode: %v", err)
	}
	if len(b) > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	return b, nil
}

// Decode parses and validates one frame. Unknown envelope fields are ignored;
// payloads are decoded separately with DecodePayload.
func Decode(b []byte) (Envelope, error) {
	if len(b) > MaxFrameBytes {
		return Envelope{}, ErrFrameTooLarge
	}
	if len(b) == 0 {
		return Envelope{}, malformed("empty frame")
	}
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, malformed("decode: %v", err)
	}
	if err := validateEnvelope(e); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// NewEnvelope builds a non-job frame at ProtocolVersion. A nil payload omits it.
func NewEnvelope(kind string, payload any) (Envelope, error) {
	return NewJobEnvelope(kind, "", 0, 0, payload)
}

// NewJobEnvelope builds a frame at ProtocolVersion with job scope.
func NewJobEnvelope(kind, jobID string, generation int64, seq uint64, payload any) (Envelope, error) {
	e := Envelope{V: ProtocolVersion, Kind: kind, JobID: jobID, Generation: generation, Seq: seq}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, malformed("encode %s payload: %v", kind, err)
		}
		e.Payload = raw
	}
	return e, nil
}

// Payload is implemented by every payload struct.
type Payload interface{ Validate() error }

// DecodePayload unmarshals e.Payload into dst and validates it. Unknown fields
// are ignored (forward compatibility); missing required fields fail Validate.
func DecodePayload(e Envelope, dst Payload) error {
	raw := bytes.TrimSpace(e.Payload)
	if len(raw) == 0 || raw[0] != '{' {
		return malformed("%s frame needs an object payload", e.Kind)
	}
	if err := json.Unmarshal(e.Payload, dst); err != nil {
		return malformed("%s payload: %v", e.Kind, err)
	}
	if err := dst.Validate(); err != nil {
		return malformed("%s payload: %v", e.Kind, err)
	}
	return nil
}
