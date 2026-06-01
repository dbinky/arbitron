package azurefoundry

import (
	"errors"
	"net/url"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

// Topology-selection and binding errors. These are returned at call time so the
// adapter fails clearly when a binding is malformed, without re-validating what
// connection-create already checks.
var (
	// ErrMissingBaseURL is returned when the binding carries no base-url.
	ErrMissingBaseURL = errors.New("azurefoundry: binding base-url is empty")
	// ErrMissingDeployment is returned for the Azure OpenAI resource topology
	// (api-version present) when the deployment name (binding Model) is empty.
	ErrMissingDeployment = errors.New("azurefoundry: resource topology requires a deployment name (binding Model)")
	// ErrMissingAPIVersion is returned when the resource topology is selected
	// but the api-version is absent. It exists for callers that pre-check the
	// resource path; endpointURL itself treats an absent api-version as the
	// serverless topology per the plan.
	ErrMissingAPIVersion = errors.New("azurefoundry: resource topology requires Extras[\"api-version\"]")
)

const chatCompletionsSuffix = "/chat/completions"

// endpointURL returns the chat-completions URL for the binding. Topology is a
// pure function of the binding fields — there is NO provider-name branching.
//
// When Extras carries a non-empty "api-version", the Azure OpenAI resource
// deployment path is used:
//
//	{base-url}/openai/deployments/{model}/chat/completions?api-version=<v>
//
// Otherwise the binding BaseURL is treated as a complete serverless endpoint
// and the chat-completions suffix is appended once (never doubled).
func endpointURL(b provider.ResolvedBinding) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
	if base == "" {
		return "", ErrMissingBaseURL
	}

	if apiVersion := strings.TrimSpace(b.Extras["api-version"]); apiVersion != "" {
		// Azure OpenAI resource topology.
		deployment := strings.TrimSpace(b.Model)
		if deployment == "" {
			return "", ErrMissingDeployment
		}
		return base + "/openai/deployments/" + url.PathEscape(deployment) +
			chatCompletionsSuffix + "?api-version=" + url.QueryEscape(apiVersion), nil
	}

	// Serverless topology: base-url IS the deployment endpoint. Append the
	// chat-completions suffix only if it is not already present.
	if strings.HasSuffix(base, chatCompletionsSuffix) {
		return base, nil
	}
	return base + chatCompletionsSuffix, nil
}
