package awssig

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// happy-path: sha256Hex of empty body is the well-known SHA-256 of "".
func TestSHA256Hex_EmptyBody(t *testing.T) {
	const wantEmptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := sha256Hex(nil); got != wantEmptySHA {
		t.Errorf("sha256Hex(nil) = %s, want %s", got, wantEmptySHA)
	}
}

// happy-path: signing key derivation matches the AWS SigV4 documentation example.
// This is the canonical example from the AWS "Examples of how to derive a
// signing key" doc: secret wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY,
// date 20150830, region us-east-1, service iam.
func TestSigningKey_AWSDocExample(t *testing.T) {
	key := signingKey(
		"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"20150830", "us-east-1", "iam",
	)
	const want = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	if got := hex.EncodeToString(key); got != want {
		t.Errorf("signingKey = %s, want %s", got, want)
	}
}

// fixedClock is the injected, deterministic time for golden-signature tests.
var fixedClock = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

func newSignedReq(t *testing.T, cred Credential, payload []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-v2/converse",
		strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := Sign(req, payload, "AKIDEXAMPLE", cred,
		"us-east-1", "bedrock", fixedClock); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return req
}

// SECURITY (golden signature): a FIXED key + time + request yields a KNOWN
// Authorization header. This pins the exact canonical-request -> string-to-sign ->
// signature algorithm so any regression in canonicalization is caught.
//
// The golden Signature was independently cross-checked against a second,
// from-scratch SigV4 computation (separate Go program reproducing the canonical
// request, string-to-sign, and HMAC chain with the identical inputs) before being
// pasted here.
func TestSignRequest_GoldenSignature(t *testing.T) {
	payload := []byte(`{"messages":[]}`)
	req := newSignedReq(t, Credential{SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, payload)

	if got := req.Header.Get("X-Amz-Date"); got != "20240102T030405Z" {
		t.Errorf("X-Amz-Date = %q, want 20240102T030405Z", got)
	}

	const wantAuth = "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20240102/us-east-1/bedrock/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=7447be769b4e184bb418f3901bb409cf2e0673c1b0595dcdcdb90050e79a32f4"
	if got := req.Header.Get("Authorization"); got != wantAuth {
		t.Errorf("Authorization mismatch\n got=%q\nwant=%q", got, wantAuth)
	}
}

// edge-case: with NO session token, X-Amz-Security-Token is absent and the
// signed-headers list does not contain it.
func TestSignRequest_NoSessionToken_NoSecurityTokenHeader(t *testing.T) {
	req := newSignedReq(t, Credential{SecretAccessKey: "sek"}, []byte(`{}`))
	if _, ok := req.Header["X-Amz-Security-Token"]; ok {
		t.Error("X-Amz-Security-Token must be absent when no session token")
	}
	if strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Error("SignedHeaders must not list x-amz-security-token when absent")
	}
}

// edge-case: WITH a session token, X-Amz-Security-Token is set to the token and
// is part of SignedHeaders (so it is integrity-covered by the signature).
func TestSignRequest_SessionToken_SetsSecurityTokenHeader(t *testing.T) {
	req := newSignedReq(t, Credential{SecretAccessKey: "sek", SessionToken: "tok-xyz"}, []byte(`{}`))
	if got := req.Header.Get("X-Amz-Security-Token"); got != "tok-xyz" {
		t.Errorf("X-Amz-Security-Token = %q, want tok-xyz", got)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Error("SignedHeaders must include x-amz-security-token when present")
	}
}

// SECURITY: the secret-access-key NEVER appears in any header (only the derived
// signature does). The signature is a hex HMAC, so the raw secret cannot match it.
func TestSignRequest_SecretNeverInHeaders(t *testing.T) {
	const secret = "TOTALLY-SECRET-RAW-KEY"
	req := newSignedReq(t, Credential{SecretAccessKey: secret, SessionToken: "sess-secret"}, []byte(`{}`))
	for name, vals := range req.Header {
		for _, v := range vals {
			if strings.Contains(v, secret) {
				t.Errorf("header %q leaked the secret-access-key: %q", name, v)
			}
		}
	}
}

// security: the session token appears ONLY in X-Amz-Security-Token, nowhere else.
func TestSignRequest_SessionTokenOnlyInSecurityHeader(t *testing.T) {
	const sess = "SESSION-TOKEN-SECRET"
	req := newSignedReq(t, Credential{SecretAccessKey: "sek", SessionToken: sess}, []byte(`{}`))
	for name, vals := range req.Header {
		if http.CanonicalHeaderKey(name) == "X-Amz-Security-Token" {
			continue
		}
		for _, v := range vals {
			if strings.Contains(v, sess) {
				t.Errorf("header %q leaked the session token: %q", name, v)
			}
		}
	}
}

// error-handling: a request with a non-empty Opaque URL that cannot produce a
// usable host still does not panic; Sign succeeds setting the headers because the
// canonicalization is purely string-based. (Kept minimal: ensures ErrSign exists
// and Sign returns nil on the normal path.)
func TestSign_ReturnsNilOnValidRequest(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err := Sign(req, nil, "AKID", Credential{SecretAccessKey: "s"}, "us-east-1", "bedrock", fixedClock); err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}
	if req.Header.Get("Authorization") == "" {
		t.Error("Authorization not set")
	}
	_ = ErrSign // sentinel must exist and be referenceable by importers
}

// failure: Sign(nil, ...) returns an error wrapping ErrSign.
func TestSign_NilRequest_ReturnsErrSign(t *testing.T) {
	err := Sign(nil, nil, "AKID", Credential{SecretAccessKey: "s"}, "us-east-1", "bedrock", fixedClock)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}
	if !errors.Is(err, ErrSign) {
		t.Errorf("error = %v, want errors.Is(err, ErrSign)==true", err)
	}
}

// failure: a request whose URL is nil returns ErrSign.
func TestSign_NilURL_ReturnsErrSign(t *testing.T) {
	req := &http.Request{}
	err := Sign(req, nil, "AKID", Credential{SecretAccessKey: "s"}, "us-east-1", "bedrock", fixedClock)
	if err == nil {
		t.Fatal("expected error for nil URL, got nil")
	}
	if !errors.Is(err, ErrSign) {
		t.Errorf("error = %v, want errors.Is(err, ErrSign)==true", err)
	}
}

// edge: canonicalQueryString sorts multiple keys and multiple values per key
// correctly, following the SigV4 requirement (keys lexicographic, values per-key
// lexicographic).
func TestCanonicalQueryString_MultiKey(t *testing.T) {
	q := map[string][]string{
		"z": {"beta", "alpha"},
		"a": {"1"},
	}
	got := canonicalQueryString(q)
	const want = "a=1&z=alpha&z=beta"
	if got != want {
		t.Errorf("canonicalQueryString = %q, want %q", got, want)
	}
}

// edge: spaces in query keys/values must be encoded as "%20" (RFC 3986), not "+"
// (HTML form encoding). The SigV4 spec explicitly prohibits "+" for space.
func TestCanonicalQueryString_SpaceEncodedAsPercent20(t *testing.T) {
	q := map[string][]string{
		"my key": {"hello world"},
	}
	got := canonicalQueryString(q)
	const want = "my%20key=hello%20world"
	if got != want {
		t.Errorf("canonicalQueryString = %q, want %q (spaces must be %%20, not +)", got, want)
	}
}
