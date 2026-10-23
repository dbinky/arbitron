package provider

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
)

// Claude 2.1.303's own usage-state sets (TO global, RO per project; see
// testdata/claude-fingerprint-state.md) name what every session rewrites on
// exit: counters, impressions, fetch times and last-session metrics. Omit only
// those. Keep MCP, env, model, feature cache contents, project tools/MCP and all
// unknown future keys.
var claudeGlobalUsageKeys = []string{
	"numStartups", "skillUsage", "pluginUsage", "memoryUsageCount", "promptQueueUseCount", "btwUseCount",
	"queuedCommandUpHintCount", "lspRecommendationIgnoredCount", "promptSuggestionUnusedStreak",
	"tipsHistory", "tipLifetimeShownCounts", "tipsHistoryByCommand", "seenNotifications", "announcementImpressions",
	"savedModelSuccessorShown", "subagentConfigWarningShown", "subagentConfigChecks", "subagentConfigClassifierCalls",
	"lastShownEmergencyTip", "subscriptionNoticeCount", "subscriptionUpsellShownCount", "passesUpsellSeenCount",
	"fullscreenUpsellSeenCount", "fullscreenDownsellSeenCount", "voiceLangHintShownCount", "voiceFooterHintSeenCount",
	"experimentNoticesSeenCount", "cachedGrowthBookFeaturesAt", "firstStartTime", "claudeCodeFirstTokenDate",
	"startupPrefetchedAt",
}

var claudeProjectUsageKeys = []string{
	"lastCost", "lastAPIDuration", "lastAPIDurationWithoutRetries", "lastToolDuration", "lastDuration", "lastStartTime",
	"lastLinesAdded", "lastLinesRemoved", "lastTotalInputTokens", "lastTotalOutputTokens",
	"lastTotalCacheCreationInputTokens", "lastTotalCacheReadInputTokens", "lastTotalWebSearchRequests",
	"lastFpsAverage", "lastFpsLow1Pct", "lastSessionId", "lastGracefulShutdown", "lastVersionBase", "lastModelUsage",
	"lastSessionMetrics", "exampleFiles", "exampleFilesGeneratedAt", "seenTeamArtifactPaths", "hasUnseenTeamArtifacts",
	"hasClaudeMdExternalIncludesWarningShown", "history", "projectOnboardingSeenCount", "hasCompletedProjectOnboarding",
	"devIntentsDetected",
}

// claudeDefaultProject is Claude 2.1.303's DEFAULT_PROJECT_CONFIG (minus the
// usage key it shares with claudeProjectUsageKeys). Claude reads a missing
// entry as these values, and saveCurrentProjectConfig creates entries from them.
var claudeDefaultProject = map[string]string{
	"allowedTools": "[]", "mcpContextUris": "[]", "mcpServers": "{}", "enabledMcpjsonServers": "[]",
	"disabledMcpjsonServers": "[]", "hasTrustDialogAccepted": "false", "hasClaudeMdExternalIncludesApproved": "false",
}

// claudeGlobalProjection keeps only the project entries a launch from
// projectDir can read. Claude looks up exactly one key, the directory's git
// root or else the directory itself, so entries other workspaces' runs create
// or rewrite never reach the probe; those runs keep their own executed-model
// check (G5). Ancestors stay because any may be the git root, and descendants
// because the launch cwd is a private child; see claudeProjectReadable.
func claudeGlobalProjection(projectDir string) func(io.Reader) ([]byte, error) {
	return func(r io.Reader) ([]byte, error) {
		state, err := codexStateObject(r) // shared strict JSON object/EOF decoder
		if err != nil {
			return nil, err
		}
		for _, key := range claudeGlobalUsageKeys {
			delete(state, key)
		}
		if account, ok := state["oauthAccount"].(map[string]any); ok {
			delete(account, "profileFetchedAt")
		}
		if projects, ok := state["projects"].(map[string]any); ok {
			readable := claudeProjectReadable(projectDir)
			for key, project := range projects {
				if !readable(key) {
					delete(projects, key)
					continue
				}
				if project, ok := project.(map[string]any); ok {
					for _, key := range claudeProjectUsageKeys {
						delete(project, key)
					}
					if claudeProjectIsDefault(project) {
						delete(projects, key)
					}
				}
			}
		}
		return json.Marshal(state)
	}
}

// claudeProjectIsDefault reports an entry Claude reads like a missing one: every
// remaining key holds its default value. Any other value keeps the whole entry.
func claudeProjectIsDefault(project map[string]any) bool {
	for key, value := range project {
		want, known := claudeDefaultProject[key]
		got, err := json.Marshal(value)
		if !known || err != nil || string(got) != want {
			return false
		}
	}
	return true
}

// claudeProjectReadable reports whether a launch under dir can read a project
// key: dir itself, an ancestor (a possible git root), or a descendant, because
// each probe launches in its own private <dir>/check-* child (G63). Both the
// given and symlink-resolved spellings count. An empty dir reads every key.
func claudeProjectReadable(dir string) func(string) bool {
	if dir == "" {
		return func(string) bool { return true }
	}
	ancestors := map[string]bool{}
	var roots []string
	for _, p := range []string{filepath.Clean(dir), resolveExisting(filepath.Clean(dir))} {
		if p == "" {
			continue
		}
		roots = append(roots, p)
		for {
			ancestors[p] = true
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	return func(key string) bool {
		if ancestors[key] {
			return true
		}
		for _, root := range roots {
			if strings.HasPrefix(key, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
}

// resolveExisting resolves symlinks in the longest existing prefix of p (the
// probe directory may not exist yet), as Claude's own realpath'd cwd would.
func resolveExisting(p string) string {
	suffix := ""
	for {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
}

// Claude OAuth tokens are opaque and its separate oauthAccount metadata is not
// authenticated by these bytes. Never erase token replacements just because the
// cached account metadata is unchanged: that would also conceal account switches.
// Keep every credential/expiry/unknown field until a stable authenticated account
// producer exists. Strict decoding refuses partial writes without leaking bytes.
func claudeCredentialProjection(r io.Reader) ([]byte, error) {
	state, err := codexStateObject(r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(state)
}
