package bedrock

import (
	"errors"
	"net/url"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

// ErrMissingRegion is returned when the binding has no non-empty
// Extras["region"] from which to derive the regional Converse endpoint host.
var ErrMissingRegion = errors.New("bedrock: binding Extras[\"region\"] is empty")

// ErrMissingModel is returned when the binding has no non-empty Model (modelId),
// which is required in the Converse endpoint path.
var ErrMissingModel = errors.New("bedrock: binding Model (modelId) is empty")

// endpointURL derives the regional Converse endpoint from Extras["region"] and
// Model. stream=false → /converse, stream=true → /converse-stream:
//
//	https://bedrock-runtime.{region}.amazonaws.com/model/{modelId}/converse[-stream]
//
// The modelId is path-escaped (model ids contain dots and, for inference
// profiles, can contain ':' and '/'). Empty region/model are rejected.
func endpointURL(b provider.ResolvedBinding, stream bool) (string, error) {
	region := strings.TrimSpace(b.Extras["region"])
	if region == "" {
		return "", ErrMissingRegion
	}
	model := strings.TrimSpace(b.Model)
	if model == "" {
		return "", ErrMissingModel
	}
	action := "converse"
	if stream {
		action = "converse-stream"
	}
	// url.PathEscape leaves ':' unescaped (it is legal in a path segment), but
	// Bedrock model ids / inference-profile ARNs embed ':' and AWS expects it
	// percent-encoded in the request URI; encode it explicitly so the value the
	// SigV4 signer canonicalizes (req.URL.EscapedPath()) matches.
	escaped := strings.ReplaceAll(url.PathEscape(model), ":", "%3A")
	return "https://bedrock-runtime." + region + ".amazonaws.com/model/" +
		escaped + "/" + action, nil
}
