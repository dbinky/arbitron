package mcp

// LunaRouteBackendHint reaches authors through the profile tools' descriptions.
// Offline backend enablement is distinct from live release verification.
const LunaRouteBackendHint = " LunaRoute: list a connection's models and their known/unknown capabilities with connection_models, then choose the model and backend explicitly; LunaRoute selections are pinned only, and an agent profile must name its backend explicitly (claude-code, opencode, codex or pi)." +
	" codex on LunaRoute runs keyed against the gateway with a private CODEX_HOME per run (never ~/.codex); the codex backend-health login verdict is informational for these runs and does not gate them." +
	" pi on LunaRoute writes a per-run private provider definition and, like claude-code and opencode, needs known context and output limits for the model in the capability table. Pi also needs known reasoning metadata from an independent evidence-backed reasoning fact; unknown required metadata is refused rather than guessed." +
	" LunaRoute requested/effective effort stays exact: generic native-provider flooring does not apply to LunaRoute; unsupported explicit settings are refused. Backend/helper live verification and release readiness remain pending in the evidence register."
