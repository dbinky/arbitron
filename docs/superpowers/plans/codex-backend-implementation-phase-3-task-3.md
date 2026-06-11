# Phase 3 / Task 3 — Prerequisite documentation

**Design:** `docs/superpowers/specs/codex-backend-design-phase-3.md`
**Depends on:** conceptually on Phases 1–2; doc-only, can land last.

## Goal
Document the codex backend's user-provided prerequisites and its security
posture. The installer is NOT modified.

## Files
- `docs/install/README.md` and/or `README.md` (edit)
- `docs/agent-security-model.md` (edit — add the codex `--yolo` posture entry)
- the deployment config example / `defaultYAMLContent` comments (edit — note the
  codex backend is subscription-only, host-login based)

## Content
1. **Prerequisites** (a "Codex backend" subsection):
   - Install the Codex CLI on the host.
   - Run `codex login` (Sign in with ChatGPT) so a valid subscription session
     exists in `~/.codex`.
   - Note the readiness check surfaces install/login state (Phase 3 Task 1).
2. **Posture / security:** the codex backend runs `codex exec --yolo` (approvals
   + sandbox bypassed) — RCE-equivalent on model-influenced prompts, accepted
   for local single-user unattended use. Cross-reference the existing
   claude-code skip-permissions and opencode allow-all posture notes in
   `docs/agent-security-model.md`.
3. **Scope note:** codex is subscription-only and agent-tier only; API-key
   OpenAI remains available via the opencode backend (per spec non-goals).

## Acceptance
- Docs build/lint clean (if a docs check exists; otherwise prose review).
- A new user can read the prerequisites and wire a codex profile without reading
  source.
- No installer files are changed.
