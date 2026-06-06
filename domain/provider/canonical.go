package provider

import "strings"

// Canonical provider ids. These are the single, domain-owned vocabulary every
// selector dispatches on after Canonicalize. Only the documented alias groups
// are collapsed; ids without an alias canonicalize to their normalized self.
const (
	// CanonicalBedrock is the canonical id for the Amazon Bedrock family
	// (aliases: "bedrock", "amazon-bedrock").
	CanonicalBedrock = "bedrock"
	// CanonicalAnthropicAPI is the canonical id for the native Anthropic
	// Messages-API family (aliases: "anthropic-api", "anthropic").
	CanonicalAnthropicAPI = "anthropic-api"
	// CanonicalAzure is the canonical id for the Azure family (aliases:
	// "azure-openai", "azure-foundry", "azure").
	CanonicalAzure = "azure"
)

// providerAliases maps a normalized (trim+lowercase) provider alias to its
// canonical id. It is the SINGLE source of truth for alias collapse, shared by
// every selector (chat/agent/probe factories and the opencode path) so the
// routing vocabulary cannot drift apart per package. Note: this is deliberately
// NOT a Family() classifier (deferred) — it only collapses spelling aliases for
// the same backend, it does not group distinct backends into families.
var providerAliases = map[string]string{
	"bedrock":        CanonicalBedrock,
	"amazon-bedrock": CanonicalBedrock,
	"anthropic-api":  CanonicalAnthropicAPI,
	"anthropic":      CanonicalAnthropicAPI,
	"azure-openai":   CanonicalAzure,
	"azure-foundry":  CanonicalAzure,
	"azure":          CanonicalAzure,
}

// Canonicalize normalizes a provider string (trim + lowercase) and collapses the
// documented spelling aliases (bedrock/amazon-bedrock, anthropic-api/anthropic,
// the azure trio) to a single canonical id. An id with no alias returns its
// normalized self; an empty/whitespace-only value returns "" (the claude-cli
// subscription path). Selectors switch on the result so a new alias is a
// one-line entry here rather than a coordinated edit across every selector.
func Canonicalize(p string) string {
	n := strings.ToLower(strings.TrimSpace(p))
	if c, ok := providerAliases[n]; ok {
		return c
	}
	return n
}
