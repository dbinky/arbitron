package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every Azure/Bedrock spelling Canonicalize collapses must be classified the
// same way by pin capture (ValidatePinRoutes) and by the separate fixed-version
// grammar in domain/modelidentity (G19). Enumerating providerAliases means a new
// alias is checked without editing this test: a chat pin takes the D8
// exception, following is refused, and an agent backend on the same route gets
// the family's own grammar — Azure never fixed, Bedrock only its fixed spelling.
func TestNativeFamilyAliasesAgreeAcrossCaptureAndVersionGrammar(t *testing.T) {
	ctx := context.Background()
	versions := modelidentity.NewVersionResolver()
	seen := map[string]bool{}
	for alias, family := range providerAliases {
		if family != CanonicalAzure && family != CanonicalBedrock {
			continue
		}
		seen[family] = true
		for _, spelling := range []string{alias, strings.ToUpper(alias), " " + alias + " "} {
			t.Run(family+"/"+spelling, func(t *testing.T) {
				request := func(model, backend string) []VersionRequest {
					return []VersionRequest{{Model: model, RouteProvider: spelling, ConnectionRef: "route", Backend: backend}}
				}
				chat := Profile{Alias: "native", Kind: ProfileKindChat, ConnectionRef: "route", Model: "Configured/Deployment:ID"}
				got, err := ValidatePinRoutes(ctx, chat, request(chat.Model, ""), versions)
				require.NoError(t, err, "a configured ID is a valid D8 pin")
				assert.Equal(t, family, got)

				following := chat
				following.SelectionMode = SelectionFollowing
				following.FollowTarget = &FollowTarget{Provider: "anthropic", Tier: "sonnet"}
				_, err = ValidatePinRoutes(ctx, following, request(chat.Model, ""), versions)
				assert.ErrorIs(t, err, ErrInvalidSelection, "native routes are pinned-only")

				for _, backend := range []string{"pi", "opencode"} {
					agent := Profile{Alias: "agent", Kind: ProfileKindAgent, Backend: backend, ConnectionRef: "route", Model: "Configured/Deployment:ID"}
					_, err = ValidatePinRoutes(ctx, agent, request(agent.Model, backend), versions)
					assert.ErrorIs(t, err, ErrSpecificVersionUnavailable, "an agent backend never inherits the D8 exception")

					agent.Model = "anthropic.claude-sonnet-4-6-v1:0"
					got, err = ValidatePinRoutes(ctx, agent, request(agent.Model, backend), versions)
					if family == CanonicalBedrock {
						require.NoError(t, err, "the version grammar must recognise every Bedrock alias")
						assert.Empty(t, got, "a fixed Bedrock version is strict, not an exception")
					} else {
						assert.ErrorIs(t, err, ErrSpecificVersionUnavailable, "the version grammar must treat every Azure alias as unversioned")
					}
				}
			})
		}
	}
	assert.True(t, seen[CanonicalAzure] && seen[CanonicalBedrock], "both native families must have aliases")
}
