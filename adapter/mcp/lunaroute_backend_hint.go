package mcp

// LunaRouteBackendHint reaches authors through the profile tools' descriptions.
// Offline backend enablement is distinct from live release verification.
const LunaRouteBackendHint = " LunaRoute: list a connection's models and their known/unknown capabilities with connection_models, then choose the model and backend explicitly; LunaRoute selections are pinned only, and an agent profile must name its backend explicitly (codex or pi). LunaRoute claude-code and opencode are not yet supported." +
	" codex on LunaRoute runs keyed against the gateway with a private CODEX_HOME per run (never ~/.codex); the codex backend-health login verdict is informational for these runs and does not gate them." +
	" pi on LunaRoute writes a per-run private provider definition and needs known context and output limits for the model in the capability table. Pi also needs known reasoning metadata from an independent evidence-backed reasoning fact; unknown required metadata is refused rather than guessed." +
	" LunaRoute requested/effective effort stays exact: generic native-provider flooring does not apply to LunaRoute; unsupported explicit settings are refused. Release readiness remains tracked separately in the evidence register."
