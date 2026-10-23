package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var errCodexState = errors.New("invalid codex account/cache state")

// Codex 0.163.0's token_data.rs defines the stable identity claims below;
// auth/storage.rs stores rotating access/refresh/ID tokens and last_refresh.
// This is an invalidation key, not JWT authentication: the harness remains the
// authority for authentication and executed-model evidence. See testdata/
// codex-fingerprint-state.md for the pinned source contract and limitations.
func codexAccountProjection(r io.Reader) ([]byte, error) {
	state, err := codexStateObject(r)
	if err != nil {
		return nil, err
	}
	if state["tokens"] == nil {
		// API-key and other auth modes retain every field, including credentials.
		return json.Marshal(state)
	}
	tokens, ok := state["tokens"].(map[string]any)
	if !ok {
		return nil, errCodexState
	}
	jwt, ok := tokens["id_token"].(string)
	if !ok {
		return nil, errCodexState
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil, errCodexState
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errCodexState
	}
	claims, err := codexStateObject(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	identity := map[string]any{}
	for _, key := range []string{"sub", "iss", "aud", "email", "https://api.openai.com/profile"} {
		if value, ok := claims[key]; ok {
			identity[key] = value
		}
	}
	authIdentity := map[string]any{}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "user_id", "chatgpt_plan_type", "chatgpt_account_is_fedramp"} {
		if value, ok := auth[key]; ok {
			authIdentity[key] = value
		}
	}
	identity["https://api.openai.com/auth"] = authIdentity
	// Never erase token changes when there is no stable workspace AND user to
	// bind them to. Unknown auth shapes remain conservatively content-hashed.
	hasString := func(m map[string]any, key string) bool {
		value, _ := m[key].(string)
		return strings.TrimSpace(value) != ""
	}
	if !(hasString(tokens, "account_id") || hasString(auth, "chatgpt_account_id")) ||
		!(hasString(claims, "sub") || hasString(auth, "chatgpt_user_id") || hasString(auth, "user_id")) {
		return json.Marshal(state)
	}
	tokens["id_token"] = identity
	delete(tokens, "access_token")
	delete(tokens, "refresh_token")
	delete(state, "last_refresh")
	return json.Marshal(state)
}

func codexCacheProjection(r io.Reader) ([]byte, error) {
	state, err := codexStateObject(r)
	if err != nil {
		return nil, err
	}
	// refresh_ttl changes only fetched_at. Keep models (including capabilities),
	// endpoint/auth identity, ETag, client version and unknown future fields.
	delete(state, "fetched_at")
	return json.Marshal(state)
}

// UseNumber avoids rounding large metadata values into the same fingerprint.
// The strict object/EOF checks refuse partial concurrent writes and trailing
// data; a failed projection never yields a partial fingerprint.
func codexStateObject(r io.Reader) (map[string]any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var state map[string]any
	if err := dec.Decode(&state); err != nil || state == nil {
		return nil, errCodexState
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, errCodexState
	}
	return state, nil
}
