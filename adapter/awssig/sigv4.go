package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrSign wraps any signing failure (e.g. an unparseable target URL). The
// secret-access-key and session token are NEVER included in the wrapped message.
var ErrSign = errors.New("awssig: sign request")

const sigv4Algorithm = "AWS4-HMAC-SHA256"

// hmacSHA256 returns HMAC-SHA256(key, data).
func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// signingKey derives the SigV4 signing key:
//
//	kDate    = HMAC("AWS4"+secret, yyyymmdd)
//	kRegion  = HMAC(kDate, region)
//	kService = HMAC(kRegion, service)
//	kSigning = HMAC(kService, "aws4_request")
func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// Sign signs req in place with AWS Signature Version 4 for the given region and
// service ("bedrock"). It sets X-Amz-Date, the Authorization header, and — only
// when cred.SessionToken != "" — X-Amz-Security-Token. The request body is
// supplied separately as payload so the body can be re-read by the HTTP client.
// now is injected for deterministic signatures.
//
// SECURITY: the secret-access-key and session token contribute ONLY to the
// derived signing key / Authorization signature and the X-Amz-Security-Token
// header; they are never placed in any other header, never logged, and never
// included in ErrSign.
func Sign(req *http.Request, payload []byte, accessKeyID string, cred Credential, region, service string, now time.Time) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("%w: nil request or URL", ErrSign)
	}

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
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalQueryString(req.URL.Query())
	payloadHash := sha256Hex(payload)

	canonical, signedHeaders := canonicalRequest(
		req.Method, canonicalURI, canonicalQuery, req.Header, host, payloadHash)

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

// canonicalQueryString builds the SigV4 canonical query: keys sorted, each value
// URL-encoded, joined by '&'. Bedrock Converse has none, so this is usually "".
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

// canonicalRequest builds the SigV4 canonical request string and returns it
// together with the semicolon-joined SignedHeaders list. Header names are
// lowercased, values trimmed, and headers sorted by name. Canonical headers
// always include host and x-amz-date (plus content-type and
// x-amz-security-token when present).
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
