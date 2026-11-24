package runnerproto

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConfirm() Dispatch {
	return Dispatch{Op: "confirm", LeaseUntil: time.Unix(0, 0), Confirmation: &WireConfirmation{
		Model: "claude-opus-5-5", Backend: "claude-code", Provider: "claude-cli", ContextWindow: "1m", Effort: "high"}}
}

func TestDispatch_ConfirmIsABoundedSecretFreeDescriptor(t *testing.T) {
	d := validConfirm()
	env, err := NewJobEnvelope(KindDispatch, "6b1f3c1e-5d2a-4c5b-9e0f-7a8b9c0d1e2f", 1, 0, &d)
	require.NoError(t, err)
	raw, err := Encode(env)
	require.NoError(t, err)
	got, err := Decode(raw)
	require.NoError(t, err)
	var back Dispatch
	require.NoError(t, DecodePayload(got, &back))
	assert.Equal(t, d.Confirmation, back.Confirmation)

	b, err := json.Marshal(d)
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &top))
	assert.ElementsMatch(t, []string{"op", "confirmation", "leaseUntil"}, mapKeys(top))
	var c map[string]any
	require.NoError(t, json.Unmarshal(top["confirmation"], &c))
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"backend", "contextWindow", "effort", "model", "provider"}, keys, "endpointFingerprint is omitted when empty")

	bad := map[string]func(*Dispatch){
		"missing descriptor": func(d *Dispatch) { d.Confirmation = nil },
		"plus chat":          func(d *Dispatch) { d.Chat = &WireChat{Model: "m", Prompt: "p", CredentialKind: "lunaroute"} },
		"missing model":      func(d *Dispatch) { d.Confirmation.Model = "" },
		"unknown backend":    func(d *Dispatch) { d.Confirmation.Backend = "opencode" },
		"missing provider":   func(d *Dispatch) { d.Confirmation.Provider = "" },
		"oversize model":     func(d *Dispatch) { d.Confirmation.Model = strings.Repeat("m", 257) },
		"bad fingerprint":    func(d *Dispatch) { d.Confirmation.EndpointFingerprint = "zz" },
		"chat op with confirm": func(d *Dispatch) {
			d.Op = "chat"
		},
	}
	for name, mutate := range bad {
		x := validConfirm()
		c := *x.Confirmation
		x.Confirmation = &c
		mutate(&x)
		assert.Error(t, x.Validate(), name)
	}
	ok := validConfirm()
	ok.Confirmation.EndpointFingerprint = strings.Repeat("a", 64)
	assert.NoError(t, ok.Validate())
}

func TestResult_ConfirmationPayload(t *testing.T) {
	exact := &WireConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}
	assert.NoError(t, (&Result{OK: true, Confirmation: exact}).Validate())
	assert.Error(t, (&Result{OK: true, Confirmation: exact, Chat: &WireChatResult{}}).Validate(), "exactly one payload")
	assert.Error(t, (&Result{OK: true, Confirmation: &WireConfirmationResult{RequestedModel: "m"}}).Validate(), "executed model required")
	assert.Error(t, (&Result{OK: true, Confirmation: &WireConfirmationResult{RequestedModel: "m", ExecutedModel: strings.Repeat("x", 257)}}).Validate())
}

func TestExecutionCapability_AcceptsConfirm(t *testing.T) {
	c := ExecutionCapability{Op: "confirm", Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: strings.Repeat("a", 64),
		Families: []string{"anthropic"}, Models: []string{"claude-opus-5-5"}, ExecutedModel: true}
	assert.NoError(t, c.Validate())
}
