# LLM Providers — Phase 3, Task 1: Bedrock SigV4 Signer + Credential-Composite Parsing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a self-contained, hand-rolled AWS SigV4 (v4) request signer for the `bedrock` service plus a credential-payload parser, both fully test-driven with a golden-signature test, as the foundation the Phase-3 Bedrock chat adapter signs requests with.

**Architecture:** A new `adapter/bedrock` package gains `sigv4.go` (canonical request → string-to-sign → signing-key derivation → `Authorization`/`X-Amz-Date`/optional `X-Amz-Security-Token` headers, with an **injectable clock** for deterministic signatures) and `credentials.go` (parse the sealed `RevealKey` payload, tolerating both the bare secret-access-key string and the JSON composite `{"secret":"...","session_token":"..."}`). No AWS SDK is pulled — the signer is hand-rolled against the documented SigV4 algorithm, a deliberate, signature-tested trade-off that keeps the dependency/supply-chain surface small.

**Tech Stack:** Go stdlib only (`crypto/hmac`, `crypto/sha256`, `encoding/hex`, `encoding/json`, `net/http`, `net/url`, `sort`, `strings`, `time`). Tests use `go test ./...` (hermetic, no network). No third-party modules.

---

## Dependencies

- **Upstream:** none functionally. Shares the connection/credential and `Config` patterns established by `adapter/openrouter` and `adapter/azurefoundry`.
- **Reference symbols (read-only, do not modify in this task):**
  - `domain/provider/provider.go` — `ResolvedBinding{Kind, Provider, BaseURL, Extras, Model, AllowedTools, RevealKey}`.
  - `adapter/openrouter/provider.go` — `Config`, `scrubControl`, `ErrUpstreamStatus` patterns (mirrored in Task 2).
- **Downstream:** Task 2 (`provider.go`, `request.go`, `response.go`, factory routing, `ValidateConnection` rule) consumes `signRequest`, `parseCredential`, and the `credential` type defined here.

## File Structure

- Create: `adapter/bedrock/sigv4.go` — the signer: `signRequest`, `signingKey`, `hmacSHA256`, `sha256Hex`, `canonicalRequest`, `canonicalHeaders`, `signedHeaderNames`. Exported sentinel `ErrSign`.
- Create: `adapter/bedrock/credentials.go` — `credential` struct, `parseCredential`, sentinel `ErrMissingSecret`.
- Test: `adapter/bedrock/sigv4_test.go`
- Test: `adapter/bedrock/credentials_test.go`

## Precise Signatures (defined in this task)

```go
// adapter/bedrock/credentials.go
package bedrock

// credential is the parsed AWS secret material the adapter signs with. The
// access-key-id is NOT here (it travels in Connection.Extras as a non-secret
// identifier); only true secrets are carried.
type credential struct {
    SecretAccessKey string // the sealed secret-access-key (required)
    SessionToken    string // optional STS session token; "" when absent
}

// ErrMissingSecret is returned when the sealed payload yields no secret-access-key.
var ErrMissingSecret = errors.New("bedrock: sealed credential payload has no secret-access-key")

// parseCredential interprets the revealed sealed string. It tolerates BOTH forms:
//   - a JSON composite object {"secret":"...","session_token":"..."} (session_token optional)
//   - a bare secret-access-key string (no session token)
// A leading '{' (after trimming whitespace) selects the JSON form; anything else
// is treated as the bare secret. An empty/whitespace-only payload, or a JSON
// object whose "secret" is empty, returns ErrMissingSecret.
func parseCredential(sealed string) (credential, error)
```

```go
// adapter/bedrock/sigv4.go
package bedrock

// ErrSign wraps any signing failure (e.g. an unparseable target URL). The
// secret-access-key and session token are NEVER included in the wrapped message.
var ErrSign = errors.New("bedrock: sign request")

// signRequest signs req in place with AWS Signature Version 4 for the given
// region and service ("bedrock"). It sets X-Amz-Date, the Authorization header,
// and — only when cred.SessionToken != "" — X-Amz-Security-Token. The request
// body is read from a fresh bytes.Reader (payload supplied separately so the
// body can be re-read by the HTTP client). now is injected for deterministic
// signatures; service is "bedrock" in production.
//
// SECURITY: the secret-access-key and session token contribute ONLY to the
// derived signing key / Authorization signature and the X-Amz-Security-Token
// header; they are never placed in any other header, never logged, and never
// included in ErrSign.
func signRequest(req *http.Request, payload []byte, accessKeyID string, cred credential, region, service string, now time.Time) error

// signingKey derives the SigV4 signing key:
//   kDate    = HMAC("AWS4"+secret, yyyymmdd)
//   kRegion  = HMAC(kDate, region)
//   kService = HMAC(kRegion, service)
//   kSigning = HMAC(kService, "aws4_request")
func signingKey(secret, dateStamp, region, service string) []byte

// hmacSHA256 returns HMAC-SHA256(key, data).
func hmacSHA256(key, data []byte) []byte

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string

// canonicalRequest builds the SigV4 canonical request string and returns it
// together with the semicolon-joined SignedHeaders list. Header names are
// lowercased, values trimmed, and headers sorted by name. Canonical headers
// always include host and x-amz-date (+ x-amz-security-token when present).
func canonicalRequest(method, canonicalURI, canonicalQuery string, headers http.Header, payloadHash string) (canonical string, signedHeaders string)
```

### Canonicalization rules the engineer MUST follow (SigV4 spec)

- **Algorithm:** `AWS4-HMAC-SHA256`.
- **Credential scope:** `{yyyymmdd}/{region}/{service}/aws4_request`.
- **`X-Amz-Date`** format: `20060102T150405Z` (UTC, `now.UTC()`).
- **Date stamp** (for signing key + scope): `20060102` (UTC).
- **Canonical URI:** the path of the target URL, already percent-encoded as the
  endpoint builder produced it (Task 2 escapes the model id). Empty path → `/`.
- **Canonical query string:** sorted `key=value` pairs, URL-encoded; Bedrock
  Converse uses no query params, so this is `""` in practice — still build it
  generically from `req.URL.Query()`.
- **Canonical headers:** include at minimum `host` and `x-amz-date`; include
  `x-amz-security-token` only when a session token is present. Each line is
  `lowercasename:trimmedvalue\n`; lines sorted by name.
- **Signed headers:** the semicolon-joined sorted lowercased header names
  (e.g. `host;x-amz-date` or `host;x-amz-date;x-amz-security-token`).
- **Payload hash:** lowercase hex SHA-256 of the exact request body bytes.
- **String-to-sign:** `"AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex(canonicalRequest)`.
- **Authorization header:**
  `AWS4-HMAC-SHA256 Credential={accessKeyID}/{scope}, SignedHeaders={signedHeaders}, Signature={hex(HMAC(signingKey, stringToSign))}`.

---

## Task 1A: Credential-payload parser

**Files:**
- Create: `adapter/bedrock/credentials.go`
- Test: `adapter/bedrock/credentials_test.go`

- [ ] **Step 1: Write the failing tests (input-validation + edge-cases + security categories)**

```go
// adapter/bedrock/credentials_test.go
package bedrock

import (
    "errors"
    "strings"
    "testing"
)

// happy-path / edge: bare secret string form (no session token).
func TestParseCredential_BareSecret(t *testing.T) {
    got, err := parseCredential("AKSECRET/raw+value")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got.SecretAccessKey != "AKSECRET/raw+value" {
        t.Errorf("secret = %q, want bare string preserved verbatim", got.SecretAccessKey)
    }
    if got.SessionToken != "" {
        t.Errorf("session token = %q, want empty for bare form", got.SessionToken)
    }
}

// edge: JSON composite form with a session token.
func TestParseCredential_JSONComposite(t *testing.T) {
    got, err := parseCredential(`{"secret":"sek-123","session_token":"tok-abc"}`)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got.SecretAccessKey != "sek-123" || got.SessionToken != "tok-abc" {
        t.Errorf("parsed = %+v, want secret=sek-123 session=tok-abc", got)
    }
}

// edge: JSON composite with leading whitespace and absent session_token.
func TestParseCredential_JSONNoSessionToken(t *testing.T) {
    got, err := parseCredential("  {\"secret\":\"only-secret\"}")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got.SecretAccessKey != "only-secret" || got.SessionToken != "" {
        t.Errorf("parsed = %+v, want secret=only-secret session=empty", got)
    }
}

// input-validation: empty / whitespace-only payload is rejected.
func TestParseCredential_EmptyRejected(t *testing.T) {
    for _, in := range []string{"", "   ", "\t\n"} {
        if _, err := parseCredential(in); !errors.Is(err, ErrMissingSecret) {
            t.Errorf("parseCredential(%q) err = %v, want ErrMissingSecret", in, err)
        }
    }
}

// input-validation: JSON object with empty "secret" is rejected.
func TestParseCredential_JSONEmptySecretRejected(t *testing.T) {
    if _, err := parseCredential(`{"secret":"","session_token":"x"}`); !errors.Is(err, ErrMissingSecret) {
        t.Errorf("err = %v, want ErrMissingSecret", err)
    }
}

// security: a malformed JSON object does not leak its raw bytes into the error.
func TestParseCredential_MalformedJSONNoLeak(t *testing.T) {
    _, err := parseCredential(`{"secret":"super-secret-value", BROKEN`)
    if err == nil {
        t.Fatal("expected an error for malformed JSON")
    }
    if strings.Contains(err.Error(), "super-secret-value") {
        t.Errorf("error leaked the secret: %v", err)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/bedrock/ -run TestParseCredential -v`
Expected: FAIL — `undefined: parseCredential`, `undefined: ErrMissingSecret`.

- [ ] **Step 3: Write minimal implementation**

```go
// adapter/bedrock/credentials.go
package bedrock

import (
    "encoding/json"
    "errors"
    "strings"
)

type credential struct {
    SecretAccessKey string
    SessionToken    string
}

var ErrMissingSecret = errors.New("bedrock: sealed credential payload has no secret-access-key")

func parseCredential(sealed string) (credential, error) {
    trimmed := strings.TrimSpace(sealed)
    if trimmed == "" {
        return credential{}, ErrMissingSecret
    }
    if strings.HasPrefix(trimmed, "{") {
        var composite struct {
            Secret       string `json:"secret"`
            SessionToken string `json:"session_token"`
        }
        if err := json.Unmarshal([]byte(trimmed), &composite); err != nil {
            // SECURITY: do not echo the payload; only a static reason.
            return credential{}, errors.New("bedrock: malformed JSON credential payload")
        }
        if composite.Secret == "" {
            return credential{}, ErrMissingSecret
        }
        return credential{SecretAccessKey: composite.Secret, SessionToken: composite.SessionToken}, nil
    }
    // Bare secret-access-key form (preserved verbatim, no session token).
    return credential{SecretAccessKey: sealed}, nil
}
```

Note: the bare form returns `sealed` (not `trimmed`) so a secret is never silently altered.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/bedrock/ -run TestParseCredential -v`
Expected: PASS (all six).

- [ ] **Step 5: Commit**

```bash
git add adapter/bedrock/credentials.go adapter/bedrock/credentials_test.go
git commit -m "feat(bedrock): credential-composite parser (bare + JSON forms)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 1B: SigV4 primitives (signing key + hashing)

**Files:**
- Create: `adapter/bedrock/sigv4.go`
- Test: `adapter/bedrock/sigv4_test.go`

- [ ] **Step 1: Write the failing tests (happy-path on primitives)**

```go
// adapter/bedrock/sigv4_test.go
package bedrock

import (
    "encoding/hex"
    "testing"
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/bedrock/ -run 'TestSHA256Hex|TestSigningKey' -v`
Expected: FAIL — `undefined: sha256Hex`, `undefined: signingKey`.

- [ ] **Step 3: Write minimal implementation of the primitives**

```go
// adapter/bedrock/sigv4.go
package bedrock

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
)

func hmacSHA256(key, data []byte) []byte {
    m := hmac.New(sha256.New, key)
    m.Write(data)
    return m.Sum(nil)
}

func sha256Hex(b []byte) string {
    sum := sha256.Sum256(b)
    return hex.EncodeToString(sum[:])
}

func signingKey(secret, dateStamp, region, service string) []byte {
    kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
    kRegion := hmacSHA256(kDate, []byte(region))
    kService := hmacSHA256(kRegion, []byte(service))
    return hmacSHA256(kService, []byte("aws4_request"))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./adapter/bedrock/ -run 'TestSHA256Hex|TestSigningKey' -v`
Expected: PASS. (If `TestSigningKey_AWSDocExample` fails, the HMAC chaining order is wrong — fix before proceeding; this is the load-bearing primitive.)

- [ ] **Step 5: Commit**

```bash
git add adapter/bedrock/sigv4.go adapter/bedrock/sigv4_test.go
git commit -m "feat(bedrock): SigV4 signing-key derivation + sha256 primitives

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 1C: Canonical request + full `signRequest` with golden signature

**Files:**
- Modify: `adapter/bedrock/sigv4.go`
- Test: `adapter/bedrock/sigv4_test.go`

- [ ] **Step 1: Write the failing golden-signature test (SECURITY category) + header-presence tests (edge-cases)**

```go
// append to adapter/bedrock/sigv4_test.go
import (
    "net/http"
    "strings"
    "testing"
    "time"
)

// fixedClock is the injected, deterministic time for golden-signature tests.
var fixedClock = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

func newSignedReq(t *testing.T, cred credential, payload []byte) *http.Request {
    t.Helper()
    req, err := http.NewRequest(http.MethodPost,
        "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-v2/converse",
        strings.NewReader(string(payload)))
    if err != nil {
        t.Fatalf("build req: %v", err)
    }
    req.Header.Set("Content-Type", "application/json")
    if err := signRequest(req, payload, "AKIDEXAMPLE", cred,
        "us-east-1", "bedrock", fixedClock); err != nil {
        t.Fatalf("signRequest: %v", err)
    }
    return req
}

// SECURITY (golden signature): a FIXED key + time + request yields a KNOWN
// Authorization header. This pins the exact canonical-request → string-to-sign →
// signature algorithm so any regression in canonicalization is caught.
//
// To generate the golden value the FIRST time: run the test, read the actual
// Authorization header from the failure message, verify it by hand against an
// independent SigV4 reference (e.g. the AWS docs walkthrough or `aws` CLI debug
// signing with the same inputs), then paste it as wantAuth. Do NOT just copy the
// produced value blindly — the point is an independent cross-check.
func TestSignRequest_GoldenSignature(t *testing.T) {
    payload := []byte(`{"messages":[]}`)
    req := newSignedReq(t, credential{SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, payload)

    if got := req.Header.Get("X-Amz-Date"); got != "20240102T030405Z" {
        t.Errorf("X-Amz-Date = %q, want 20240102T030405Z", got)
    }

    const wantAuth = "AWS4-HMAC-SHA256 " +
        "Credential=AKIDEXAMPLE/20240102/us-east-1/bedrock/aws4_request, " +
        "SignedHeaders=content-type;host;x-amz-date, " +
        "Signature=<FILL-AFTER-INDEPENDENT-VERIFICATION>"
    if got := req.Header.Get("Authorization"); got != wantAuth {
        t.Errorf("Authorization mismatch\n got=%q\nwant=%q", got, wantAuth)
    }
}

// edge-case: with NO session token, X-Amz-Security-Token is absent and the
// signed-headers list does not contain it.
func TestSignRequest_NoSessionToken_NoSecurityTokenHeader(t *testing.T) {
    req := newSignedReq(t, credential{SecretAccessKey: "sek"}, []byte(`{}`))
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
    req := newSignedReq(t, credential{SecretAccessKey: "sek", SessionToken: "tok-xyz"}, []byte(`{}`))
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
    req := newSignedReq(t, credential{SecretAccessKey: secret, SessionToken: "sess-secret"}, []byte(`{}`))
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
    req := newSignedReq(t, credential{SecretAccessKey: "sek", SessionToken: sess}, []byte(`{}`))
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/bedrock/ -run TestSignRequest -v`
Expected: FAIL — `undefined: signRequest` (and `canonicalRequest`).

- [ ] **Step 3: Write the canonicalization + `signRequest` implementation**

```go
// append to adapter/bedrock/sigv4.go
import (
    "errors"
    "fmt"
    "net/http"
    "net/url"
    "sort"
    "strings"
    "time"
)

var ErrSign = errors.New("bedrock: sign request")

const sigv4Algorithm = "AWS4-HMAC-SHA256"

func signRequest(req *http.Request, payload []byte, accessKeyID string, cred credential, region, service string, now time.Time) error {
    t := now.UTC()
    amzDate := t.Format("20060102T150405Z")
    dateStamp := t.Format("20060102")

    // Host + x-amz-date are always signed; security-token is signed when present.
    req.Header.Set("X-Amz-Date", amzDate)
    if cred.SessionToken != "" {
        req.Header.Set("X-Amz-Security-Token", cred.SessionToken)
    }
    if req.Host == "" {
        req.Host = req.URL.Host
    }

    canonicalURI := req.URL.EscapedPath()
    if canonicalURI == "" {
        canonicalURI = "/"
    }
    canonicalQuery := canonicalQueryString(req.URL.Query())
    payloadHash := sha256Hex(payload)

    canonical, signedHeaders := canonicalRequest(
        req.Method, canonicalURI, canonicalQuery, req.Header, req.URL.Host, payloadHash)

    scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
    stringToSign := strings.Join([]string{
        sigv4Algorithm,
        amzDate,
        scope,
        sha256Hex([]byte(canonical)),
    }, "\n")

    key := signingKey(cred.SecretAccessKey, dateStamp, region, service)
    signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

    req.Header.Set("Authorization", fmt.Sprintf(
        "%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
        sigv4Algorithm, accessKeyID, scope, signedHeaders, signature))
    return nil
}

// canonicalQueryString builds the SigV4 canonical query: keys sorted, each
// value URL-encoded, joined by '&'. Bedrock Converse has none, so this is
// usually "".
func canonicalQueryString(q url.Values) string {
    if len(q) == 0 {
        return ""
    }
    keys := make([]string, 0, len(q))
    for k := range q {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    var parts []string
    for _, k := range keys {
        vals := append([]string(nil), q[k]...)
        sort.Strings(vals)
        for _, v := range vals {
            parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
        }
    }
    return strings.Join(parts, "&")
}

func canonicalRequest(method, canonicalURI, canonicalQuery string, headers http.Header, host, payloadHash string) (string, string) {
    // Collect the headers we sign: host + x-amz-date + content-type (+ security-token).
    signed := map[string]string{
        "host":       host,
        "x-amz-date": strings.TrimSpace(headers.Get("X-Amz-Date")),
    }
    if ct := strings.TrimSpace(headers.Get("Content-Type")); ct != "" {
        signed["content-type"] = ct
    }
    if st := strings.TrimSpace(headers.Get("X-Amz-Security-Token")); st != "" {
        signed["x-amz-security-token"] = st
    }

    names := make([]string, 0, len(signed))
    for n := range signed {
        names = append(names, n)
    }
    sort.Strings(names)

    var canonicalHeaders strings.Builder
    for _, n := range names {
        canonicalHeaders.WriteString(n)
        canonicalHeaders.WriteByte(':')
        canonicalHeaders.WriteString(signed[n])
        canonicalHeaders.WriteByte('\n')
    }
    signedHeaders := strings.Join(names, ";")

    canonical := strings.Join([]string{
        method,
        canonicalURI,
        canonicalQuery,
        canonicalHeaders.String(),
        signedHeaders,
        payloadHash,
    }, "\n")
    return canonical, signedHeaders
}
```

Note: `canonicalRequest`'s signature here takes `host` explicitly (the version in the Precise-Signatures section is illustrative; this implementation threads `host` and `payloadHash` for testability). Keep this concrete signature.

- [ ] **Step 4: Run tests; fill the golden Authorization value**

Run: `go test ./adapter/bedrock/ -run TestSignRequest -v`
Expected: all PASS **except** `TestSignRequest_GoldenSignature`, which fails showing the produced `Signature=...`. Verify that produced signature independently (AWS SigV4 walkthrough / `aws` CLI signing-debug with identical inputs), then paste the verified hex into `wantAuth` and re-run.
Expected after fill: PASS.

- [ ] **Step 5: Run the full package + vet**

Run: `go test ./adapter/bedrock/ -v && go vet ./adapter/bedrock/`
Expected: PASS, no vet findings.

- [ ] **Step 6: Commit**

```bash
git add adapter/bedrock/sigv4.go adapter/bedrock/sigv4_test.go
git commit -m "feat(bedrock): hand-rolled SigV4 signer with golden-signature test

Canonical request -> string-to-sign -> signature with injectable clock.
X-Amz-Security-Token only when a session token is present; secret and
session token never leak into other headers. No AWS SDK dependency.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Acceptance Criteria (Task 1)

- `adapter/bedrock/credentials.go` parses **both** the bare secret string and the JSON composite `{"secret","session_token"}`; empty payload / empty `"secret"` → `ErrMissingSecret`; malformed JSON error does **not** echo the payload.
- `adapter/bedrock/sigv4.go` derives the signing key matching the **AWS documentation example** (`TestSigningKey_AWSDocExample` green) and produces a stable, independently-verified golden `Authorization` header for fixed key+time+request.
- `X-Amz-Security-Token` is set **iff** a session token is present, and is part of `SignedHeaders` when present.
- The secret-access-key never appears in any header; the session token appears **only** in `X-Amz-Security-Token`.
- Time is injected (`now time.Time`) — no `time.Now()` inside the signer.
- No third-party dependency added (verify `git diff go.mod` is empty).
- `go test ./adapter/bedrock/...` and `go vet ./adapter/bedrock/...` pass; `make test` (== `go test ./...`) stays green.

## Five-Category Coverage Map (Task 1)

- **Happy-path:** `TestSHA256Hex_EmptyBody`, `TestSigningKey_AWSDocExample`, `TestParseCredential_BareSecret`.
- **Input-validation:** `TestParseCredential_EmptyRejected`, `TestParseCredential_JSONEmptySecretRejected`.
- **Error-handling:** `TestParseCredential_MalformedJSONNoLeak` (malformed JSON path).
- **Edge-cases:** `TestParseCredential_JSONComposite`, `TestParseCredential_JSONNoSessionToken`, `TestSignRequest_NoSessionToken_NoSecurityTokenHeader`, `TestSignRequest_SessionToken_SetsSecurityTokenHeader`.
- **Security:** `TestSignRequest_GoldenSignature`, `TestSignRequest_SecretNeverInHeaders`, `TestSignRequest_SessionTokenOnlyInSecurityHeader`, `TestParseCredential_MalformedJSONNoLeak`.
