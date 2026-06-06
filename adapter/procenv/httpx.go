package procenv

import (
	"fmt"
	"io"
	"strings"
)

// ErrResponseTooLarge guards the bounded read: it is returned (instead of EOF)
// when a response body exceeds the configured cap, so an over-large body is a
// hard failure rather than a silent truncation. SECURITY: this bounds the OOM
// DoS from an attacker-pointed base_url streaming an unbounded body.
var ErrResponseTooLarge = fmt.Errorf("procenv: response exceeds max bytes")

// limitedReader is like io.LimitReader but returns ErrResponseTooLarge (rather
// than EOF) once the cap is exceeded.
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		return int(l.remaining + int64(n)), ErrResponseTooLarge
	}
	return n, err
}

// BoundedReader caps r at max bytes; max <= 0 returns r unchanged. Reading past
// the cap yields ErrResponseTooLarge. It is the single shared bounded-read used
// by the credential-bearing HTTP adapters (anthropicapi, bedrock, openrouter,
// azurefoundry) and the connection probes as an OOM/DoS backstop.
func BoundedReader(r io.Reader, max int64) io.Reader {
	if max <= 0 {
		return r
	}
	return &limitedReader{r: r, remaining: max}
}

// ScrubControl replaces ASCII control characters (including tab/newline/CR and
// DEL) with a space so an upstream-shaped error snippet cannot inject newlines,
// carriage returns, or terminal escapes into structured logs. Printable bytes
// and valid UTF-8 multibyte sequences are preserved. SECURITY: this is a
// log-injection defense applied on every adapter/probe error path.
func ScrubControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0 && r < 0x20) || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// statusSnippetMax caps the upstream error snippet included in classified
// errors. It bounds how much hostile upstream body reaches logs.
const statusSnippetMax = 512

// StatusSnippet reads body, caps it at 512 bytes, and control-scrubs the result
// for safe inclusion in a non-2xx error message. It NEVER carries a credential
// (callers only pass the upstream response body). A read error yields whatever
// bytes were read before the failure (best-effort diagnostics).
func StatusSnippet(body io.Reader) string {
	snippet, _ := io.ReadAll(body)
	if len(snippet) > statusSnippetMax {
		snippet = snippet[:statusSnippetMax]
	}
	return ScrubControl(string(snippet))
}
