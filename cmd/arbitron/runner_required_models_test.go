package main

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type requiredModelProfiles struct {
	profiles []provider.Profile
	err      error
}

func (p requiredModelProfiles) List(context.Context) ([]provider.Profile, error) {
	return p.profiles, p.err
}

type requiredModelResolver map[string]string

func (r requiredModelResolver) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	if m, ok := r[alias]; ok {
		return provider.ResolvedBinding{Provider: "claude-cli", Model: m}, nil
	}
	return provider.ResolvedBinding{}, provider.ErrUnknownProfile
}
func TestProfileClaudeModels_OnlySubscriptionChatProfilesResolvedToAnthropic(t *testing.T) {
	p := profileClaudeModels{profiles: requiredModelProfiles{profiles: []provider.Profile{
		{Alias: "opus", Kind: provider.ProfileKindChat},
		{Alias: "duplicate", Kind: provider.ProfileKindChat},
		{Alias: "connected", Kind: provider.ProfileKindChat, ConnectionRef: "azure-1"},
		{Alias: "agent", Kind: provider.ProfileKindAgent},
		{Alias: "gpt", Kind: provider.ProfileKindChat},
		{Alias: "missing", Kind: provider.ProfileKindChat},
	}}, resolver: requiredModelResolver{"opus": "claude-opus-5-5", "duplicate": "claude-opus-5-5", "connected": "claude-sonnet-4-5", "agent": "claude-sonnet-4-5", "gpt": "gpt-5.5"}}
	got, err := p.RequiredClaudeModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5-5"}, got)
}
func TestProfileClaudeModels_ListError(t *testing.T) {
	sentinel := errors.New("profiles unavailable")
	p := profileClaudeModels{profiles: requiredModelProfiles{err: sentinel}, resolver: requiredModelResolver{}}
	got, err := p.RequiredClaudeModels(context.Background())
	assert.ErrorIs(t, err, sentinel)
	assert.Nil(t, got)
}
