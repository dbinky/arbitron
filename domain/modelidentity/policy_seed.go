package modelidentity

// InitialRules is the shipped D2 table (decisions D2, "Initial table"). The
// 025_model_identity_policy migration seeds the same rows into SQLite; a test
// keeps the two identical. A catalog ID matching none of these stays catalogued
// but is never followable.
func InitialRules() []PatternRule {
	anthropic := func(tier string) PatternRule {
		// Four/eight-digit snapshot suffixes must not become minor versions.
		// Keep the default conservative; admins can extend the editable table.
		return PatternRule{Providers: []string{"anthropic"}, Tier: tier, Pattern: `claude-` + tier + `-(\d{1,3})(?:-(\d{1,3}))?`, VersionGroups: []int{1, 2}}
	}
	openai := func(tier string) PatternRule {
		return PatternRule{Providers: []string{"openai"}, Tier: tier, Pattern: `gpt-(\d+(?:\.\d+)?)-` + tier, VersionGroups: []int{1}}
	}
	deepseek := func(tier string) PatternRule {
		return PatternRule{Providers: []string{"deepseek", "ollama-cloud", "openrouter"}, Tier: tier, Pattern: `deepseek-v(\d+(?:\.\d+)?)-` + tier, VersionGroups: []int{1}}
	}
	return []PatternRule{
		anthropic("opus"), anthropic("sonnet"), anthropic("haiku"), anthropic("fable"),
		openai("sol"), openai("astra"), openai("luna"), openai("terra"),
		deepseek("flash"), deepseek("pro"),
		{Providers: []string{"zhipuai", "ollama-cloud"}, Tier: "glm", Pattern: `glm-(\d+(?:\.\d+)?)`, VersionGroups: []int{1}},
		{Providers: []string{"zhipuai", "ollama-cloud"}, Tier: "glm-flash", Pattern: `glm-(\d+(?:\.\d+)?)-flash`, VersionGroups: []int{1}},
		{Providers: []string{"moonshotai", "ollama-cloud"}, Tier: "kimi", Pattern: `kimi-k(\d+(?:\.\d+)?)`, VersionGroups: []int{1}},
		{Providers: []string{"moonshotai", "ollama-cloud"}, Tier: "kimi-code", Pattern: `kimi-k(\d+(?:\.\d+)?)-code`, VersionGroups: []int{1}},
	}
}
