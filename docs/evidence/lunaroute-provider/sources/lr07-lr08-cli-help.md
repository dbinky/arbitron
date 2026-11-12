# LR-07/LR-08 — Installed CLI help surface (not runtime contracts)

Accessed: 2026-11-12 UTC. Sources: the installed executables' `--version`,
`codex exec --help` and `pi --help`, invoked by `TestLiveCodexHelp` and
`TestLivePiHelp` in `test/lunaroute/backends_live_test.go`.

Reference documentation:
- Codex CLI reference: https://developers.openai.com/codex/cli/reference
- Pi CLI: https://github.com/earendil-works/pi (installed package
  `@earendil-works/pi-coding-agent`)

## Observed surface

- `codex-cli 0.167.1`: usage is `codex exec [OPTIONS] [PROMPT]`.
  Its Options section advertises `--config`, `--model`,
  `--dangerously-bypass-approvals-and-sandbox`, `--cd` and `--json`.
- Pi `1.5.2`: usage begins `pi [options]`. Its Options section advertises
  `--model`, `--mode`, `--print`, `--no-session` and `--thinking`.

These installed versions differ from the decisions' historical verified-on
inventory. There is no runtime version gate. A binary upgrade still requires
full contract/helper experiments before release; a help pass does not supply
those observations.

## Isolation and collection boundary

The collector resolves the executable once and runs both bounded queries with
that executable, the same empty private workspace and an explicit credential-free
environment. HOME, TMPDIR, XDG config/data/cache and the backend's config-directory
pointer all address fresh owner-only directories. Pi also receives its documented
`PI_OFFLINE=1` for these non-inference queries. Only PATH is retained from the
parent; no provider credential, proxy, runtime injection or ambient config pointer
is forwarded. Private directories are removed before returning a successful
projection, and cleanup failure refuses it.

Only validated version text and exact required option names leave the collector.
Help prose, stderr, config bytes and process errors are never logged or written.
Missing options, empty/oversized output, failed launches and cancellation fail
with fixed safe text. Local missing binaries explicitly skip with readiness
pending; release mode fails missing binaries.

## What this does not establish

No inference, tool task or forced helper ran. Help advertises spellings, not
whether provider overrides, metadata, relocation, credential independence or
supporting-call routing actually work. These queries do not establish project
trust behavior, enforceable limits or auth isolation during inference. They
publish no contract/run artifact, supported capability or verified register
status. The complete `TestLiveCodexLunaRoute` and `TestLivePiLunaRoute` collectors
remain required by P4-T1.
