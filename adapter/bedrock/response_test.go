package bedrock

import (
	"testing"
)

// happy-path: joined assistant text + usage.
func TestDecodeConverse_HappyPath(t *testing.T) {
	body := []byte(`{
      "output":{"message":{"role":"assistant","content":[{"text":"Hello "},{"text":"world"}]}},
      "usage":{"inputTokens":11,"outputTokens":7}
    }`)
	text, in, out, err := decodeConverse(body)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if in != 11 || out != 7 {
		t.Errorf("tokens = %d/%d, want 11/7", in, out)
	}
}

// error-handling: empty body rejected.
func TestDecodeConverse_EmptyBody(t *testing.T) {
	if _, _, _, err := decodeConverse([]byte("  ")); err == nil {
		t.Fatal("expected error for empty body")
	}
}

// error-handling: malformed JSON rejected.
func TestDecodeConverse_MalformedJSON(t *testing.T) {
	if _, _, _, err := decodeConverse([]byte(`{"output": BROKEN`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// edge-case: a message with no text content is rejected (no silent empty result).
func TestDecodeConverse_NoTextContent(t *testing.T) {
	body := []byte(`{"output":{"message":{"role":"assistant","content":[]}},"usage":{}}`)
	if _, _, _, err := decodeConverse(body); err == nil {
		t.Fatal("expected error when message has no text content")
	}
}

// happy-path: contentBlockDelta -> token, metadata -> usage, messageStop -> done.
func TestParseConverseEvent_DeltaUsageStop(t *testing.T) {
	delta, _, _, _, err := parseConverseEvent([]byte(`{"contentBlockDelta":{"delta":{"text":"tok"}}}`))
	if err != nil || delta != "tok" {
		t.Fatalf("delta = %q err = %v", delta, err)
	}
	_, in, out, _, err := parseConverseEvent([]byte(`{"metadata":{"usage":{"inputTokens":3,"outputTokens":4}}}`))
	if err != nil || in != 3 || out != 4 {
		t.Fatalf("usage = %d/%d err = %v", in, out, err)
	}
	_, _, _, done, err := parseConverseEvent([]byte(`{"messageStop":{"stopReason":"end_turn"}}`))
	if err != nil || !done {
		t.Fatalf("done = %v err = %v", done, err)
	}
}

// edge-case: an unrecognized/blank event is ignored (all-zero return, no error).
func TestParseConverseEvent_IgnoredEvent(t *testing.T) {
	delta, in, out, done, err := parseConverseEvent([]byte(`{"messageStart":{"role":"assistant"}}`))
	if err != nil || delta != "" || in != 0 || out != 0 || done {
		t.Fatalf("unexpected non-zero for ignored event: %q %d %d %v %v", delta, in, out, done, err)
	}
}
