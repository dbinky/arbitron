package awssig

import (
	"errors"
	"strings"
	"testing"
)

// happy-path / edge: bare secret string form (no session token).
func TestParseCredential_BareSecret(t *testing.T) {
	got, err := ParseCredential("AKSECRET/raw+value")
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
	got, err := ParseCredential(`{"secret":"sek-123","session_token":"tok-abc"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SecretAccessKey != "sek-123" || got.SessionToken != "tok-abc" {
		t.Errorf("parsed = %+v, want secret=sek-123 session=tok-abc", got)
	}
}

// edge: JSON composite with leading whitespace and absent session_token.
func TestParseCredential_JSONNoSessionToken(t *testing.T) {
	got, err := ParseCredential("  {\"secret\":\"only-secret\"}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SecretAccessKey != "only-secret" || got.SessionToken != "" {
		t.Errorf("parsed = %+v, want secret=only-secret session=empty", got)
	}
}

// edge: BuildCredential round-trips through ParseCredential (both forms).
func TestBuildCredential_RoundTrips(t *testing.T) {
	bare := BuildCredential("only-secret", "")
	if bare != "only-secret" {
		t.Errorf("BuildCredential bare = %q, want bare string", bare)
	}
	composite := BuildCredential("sek-1", "tok-1")
	got, err := ParseCredential(composite)
	if err != nil || got.SecretAccessKey != "sek-1" || got.SessionToken != "tok-1" {
		t.Errorf("round-trip = %+v err=%v, want secret=sek-1 session=tok-1", got, err)
	}
}

// input-validation: empty / whitespace-only payload is rejected.
func TestParseCredential_EmptyRejected(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if _, err := ParseCredential(in); !errors.Is(err, ErrMissingSecret) {
			t.Errorf("ParseCredential(%q) err = %v, want ErrMissingSecret", in, err)
		}
	}
}

// input-validation: JSON object with empty "secret" is rejected.
func TestParseCredential_JSONEmptySecretRejected(t *testing.T) {
	if _, err := ParseCredential(`{"secret":"","session_token":"x"}`); !errors.Is(err, ErrMissingSecret) {
		t.Errorf("err = %v, want ErrMissingSecret", err)
	}
}

// security: a malformed JSON object does not leak its raw bytes into the error.
func TestParseCredential_MalformedJSONNoLeak(t *testing.T) {
	_, err := ParseCredential(`{"secret":"super-secret-value", BROKEN`)
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Errorf("error leaked the secret: %v", err)
	}
}

// edge: a JSON composite where the "secret" key is absent (not empty — absent)
// is rejected with ErrMissingSecret because json.Unmarshal leaves the field at
// its zero value "".
func TestParseCredential_JSONAbsentSecretRejected(t *testing.T) {
	if _, err := ParseCredential(`{"session_token":"tok"}`); !errors.Is(err, ErrMissingSecret) {
		t.Errorf("err = %v, want ErrMissingSecret when 'secret' key is absent", err)
	}
}

// edge: a secret or session token containing JSON-special characters (double
// quotes, backslashes) must round-trip through BuildCredential+ParseCredential
// without corruption. A bug here would cause silent AWS auth failures.
func TestBuildCredential_SpecialCharsRoundTrip(t *testing.T) {
	secret := `sek/"\\with"quotes`
	session := `tok/"\\with"session`
	sealed := BuildCredential(secret, session)
	got, err := ParseCredential(sealed)
	if err != nil {
		t.Fatalf("ParseCredential error: %v", err)
	}
	if got.SecretAccessKey != secret || got.SessionToken != session {
		t.Errorf("round-trip mismatch:\n got secret=%q session=%q\nwant secret=%q session=%q",
			got.SecretAccessKey, got.SessionToken, secret, session)
	}
}

// edge: bare string form preserves the input verbatim, including any leading or
// trailing whitespace. Callers are responsible for storing clean values; the
// parser does not silently strip whitespace from the non-JSON path.
func TestParseCredential_BareStringVerbatim(t *testing.T) {
	const withSpaces = "  AKEXAMPLE  "
	got, err := ParseCredential(withSpaces)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SecretAccessKey != withSpaces {
		t.Errorf("SecretAccessKey = %q, want verbatim %q", got.SecretAccessKey, withSpaces)
	}
}
