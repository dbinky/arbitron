package bedrock

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

func binding(region, model string) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "bedrock",
		Extras:   map[string]string{"region": region, "access-key-id": "AKID"},
		Model:    model,
	}
}

// happy-path: region drives the host; non-stream uses /converse.
func TestEndpointURL_Converse(t *testing.T) {
	got, err := endpointURL(binding("us-east-1", "anthropic.claude-3-sonnet-20240229-v1:0"), false)
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-sonnet-20240229-v1%3A0/converse"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// edge-case: streaming variant appends /converse-stream.
func TestEndpointURL_ConverseStream(t *testing.T) {
	got, err := endpointURL(binding("eu-west-1", "anthropic.claude-v2"), true)
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://bedrock-runtime.eu-west-1.amazonaws.com/model/anthropic.claude-v2/converse-stream"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// edge-case: a different region changes only the host segment.
func TestEndpointURL_RegionDrivesHost(t *testing.T) {
	got, _ := endpointURL(binding("ap-southeast-2", "amazon.titan-text-express-v1"), false)
	const want = "https://bedrock-runtime.ap-southeast-2.amazonaws.com/model/amazon.titan-text-express-v1/converse"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// input-validation: empty region rejected.
func TestEndpointURL_MissingRegion(t *testing.T) {
	if _, err := endpointURL(binding("", "m"), false); !errors.Is(err, ErrMissingRegion) {
		t.Fatalf("err = %v, want ErrMissingRegion", err)
	}
}

// input-validation: empty model rejected.
func TestEndpointURL_MissingModel(t *testing.T) {
	if _, err := endpointURL(binding("us-east-1", "  "), false); !errors.Is(err, ErrMissingModel) {
		t.Fatalf("err = %v, want ErrMissingModel", err)
	}
}
