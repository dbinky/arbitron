package runnerproto

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sessJob = "6f1c1f0e-8a39-4d0e-9d51-3a4c3c0b6a11"

func wireBundle() *WireSessionBundle {
	return &WireSessionBundle{Backend: "claude-code", SessionID: "s-old", HarnessVersion: "2.1.3",
		OriginWorkDir: "/srv/arbitron/workspaces/r1", Checksum: strings.Repeat("ab", 32), Size: 12}
}

func importDispatch() Dispatch {
	return Dispatch{Op: OpSessionImport, LeaseUntil: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		Session: &WireSessionOp{Backend: "claude-code", SessionID: "s-old", WorkDir: "/srv/arbitron/workspaces/r1",
			TransferToken: "rtt_x", Bundle: wireBundle()}}
}

func wireResource() *WireResource {
	return &WireResource{ID: uuid.NewString(), Kind: "resolution", WorkDir: "/srv/arbitron/workspaces/.runner-resources/resolutions/x",
		FenceKey: "resolution:x", FenceOwner: "mac-a", FenceGeneration: 2, FenceToken: uuid.NewString(), CheckpointRef: "x@3:" + strings.Repeat("c", 64)}
}

func nativeStream() WireChat {
	return WireChat{Model: "claude-opus-5-5", Prompt: "next", CredentialKind: "anthropic_subscription", Stream: true,
		SessionID: "s1", Backend: "claude-code", WorkDir: "/srv/arbitron/workspaces/r1", ProfileKind: "agent",
		NativeCheckpoint: true, SessionExportToken: "rtt_session"}
}

func TestSessionImport_EnvelopeRoundTrip(t *testing.T) {
	d := importDispatch()
	kind, ok := SessionKindForOp(OpSessionImport)
	require.True(t, ok)
	assert.Equal(t, KindSessionImport, kind)
	env, err := NewJobEnvelope(kind, sessJob, 3, 0, &d)
	require.NoError(t, err)
	raw, err := Encode(env)
	require.NoError(t, err)
	back, err := Decode(raw)
	require.NoError(t, err, "session_import is a known, job-scoped kind")
	var got Dispatch
	require.NoError(t, DecodePayload(back, &got))
	assert.Equal(t, d.Session, got.Session)
	assert.Nil(t, got.Chat)

	for _, op := range []string{"stream", "session_export"} {
		_, ok := SessionKindForOp(op)
		assert.False(t, ok, "%s is not a separately slotted session op (contracts §24)", op)
	}
	env, err = NewJobEnvelope("session_export", sessJob, 3, 0, &d)
	if err == nil {
		raw, err = Encode(env)
		if err == nil {
			_, err = Decode(raw)
		}
	}
	assert.ErrorIs(t, err, ErrMalformed, "the retired session_export kind is unknown")
}

func TestSessionImport_Validate(t *testing.T) {
	d := importDispatch()
	assert.NoError(t, d.Validate())
	for name, mutate := range map[string]func(*Dispatch){
		"no session payload": func(d *Dispatch) { d.Session = nil },
		"chat alongside session": func(d *Dispatch) {
			d.Chat = &WireChat{Model: "m", Prompt: "p", CredentialKind: "anthropic_subscription"}
		},
		"relative workdir":      func(d *Dispatch) { d.Session.WorkDir = "workspaces/r1" },
		"no transfer token":     func(d *Dispatch) { d.Session.TransferToken = "" },
		"no backend":            func(d *Dispatch) { d.Session.Backend = "" },
		"import without bundle": func(d *Dispatch) { d.Session.Bundle = nil },
		"non-hex checksum":      func(d *Dispatch) { d.Session.Bundle.Checksum = "xyz" },
		"empty bundle":          func(d *Dispatch) { d.Session.Bundle.Size = 0 },
		"backend mismatch":      func(d *Dispatch) { d.Session.Bundle.Backend = "codex" },
		"invalid resource":      func(d *Dispatch) { d.Resource = &WireResource{Kind: "resolution"} },
	} {
		d := importDispatch()
		mutate(&d)
		assert.Error(t, d.Validate(), name)
	}
	d = importDispatch()
	d.Resource = wireResource()
	assert.NoError(t, d.Validate(), "a resolution session import names its resource")
}

func TestWireChat_SessionFields(t *testing.T) {
	c := nativeStream()
	assert.NoError(t, c.Validate())
	for name, mutate := range map[string]func(*WireChat){
		"relative workdir":                  func(c *WireChat) { c.WorkDir = "relative/dir" },
		"bad profile kind":                  func(c *WireChat) { c.ProfileKind = "batch" },
		"native checkpoint without token":   func(c *WireChat) { c.SessionExportToken = "" },
		"token without native checkpoint":   func(c *WireChat) { c.NativeCheckpoint = false },
		"native checkpoint on a chat op":    func(c *WireChat) { c.Stream = false },
		"native checkpoint without workdir": func(c *WireChat) { c.WorkDir = "" },
	} {
		c := nativeStream()
		mutate(&c)
		assert.Error(t, c.Validate(), name)
	}
	stateless := nativeStream()
	stateless.NativeCheckpoint, stateless.SessionExportToken = false, ""
	assert.NoError(t, stateless.Validate(), "stateless API chat commits its transcript; no native bundle")
}

func TestStreamDispatch_ResourceDescriptor(t *testing.T) {
	c := nativeStream()
	d := Dispatch{Op: "stream", Chat: &c, Resource: wireResource()}
	assert.NoError(t, d.Validate())
	d.Resource.WorkDir = c.WorkDir + "/../escape"
	assert.Error(t, d.Validate(), "the resource workdir is a clean absolute path")
	d.Resource = wireResource()
	d.Resource.FenceGeneration = 0
	assert.Error(t, d.Validate(), "a resource descriptor carries its complete fence")
	d.Resource = wireResource()
	d.Resource.Kind = "parent"
	assert.Error(t, d.Validate())
}

func TestStreamResult_CarriesSessionDescriptor(t *testing.T) {
	r := Result{OK: true, Chat: &WireChatResult{Output: "x", SessionID: "s1", Session: wireBundle()}}
	r.Chat.Session.SessionID = "s1"
	assert.NoError(t, r.Validate(), "a native turn result carries its uploaded bundle descriptor beside the chat payload")
	r.Chat.Session.Size = -1
	assert.Error(t, r.Validate())
	r = Result{OK: true, Chat: &WireChatResult{Output: "x", SessionID: "s1", Session: wireBundle()}}
	assert.Error(t, r.Validate(), "the bundle names the turn's session")
}

func TestSessionImportResult_Validate(t *testing.T) {
	r := Result{OK: true, Session: &WireSessionResult{SessionID: "s-new"}}
	assert.NoError(t, r.Validate())
	r = Result{OK: true, Session: &WireSessionResult{}}
	assert.Error(t, r.Validate(), "an import result names the resumable session")
	r = Result{OK: true, Session: &WireSessionResult{SessionID: "s"}, Chat: &WireChatResult{Output: "x"}}
	assert.Error(t, r.Validate(), "two payloads")
}
