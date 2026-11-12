# LR-08 — Pi resolved-model RPC controls (partial)

Inspected 2026-11-12. This source/offline experiment establishes **resolved RPC
model configuration only**, not the complete LR-08 contract, paid inference,
Bearer authentication, JSON-mode mission behavior, helpers, tool completion or
backend readiness.

## Reviewed sources

Installed package: `@earendil-works/pi-coding-agent` **1.5.2**, under
`/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent`.
Reference URLs (not fetched for this inspection):

- <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/configuration.md>
- <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models-configuration.md>
- <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md>
- <https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-commands.md>

Inspected installed bytes, with SHA-256 (distribution/source identity only;
never credential hashes):

| File | SHA-256 |
|---|---|
| `package.json` | `18b422b7d80f2caa2551d253c0a8409e1caa54b954bfb7d18252e507bb8a0318` |
| `dist/config.js` | `8d84a58d53f1bd4360188f787b56b16444c20e00452d075cdc77056054387926` |
| `dist/core/provider-composer.js` | `98facdd61bdb741804f8f303c5b2661db7f40b64a8ec0ef4767df0d71ec03608` |
| `dist/modes/rpc/rpc-mode.js` | `505297dba2df1de3003c0399b3312fd581359a4e27a5e9262b274b6cb5cb4ef3` |
| `dist/main.js` | `29d5564f1a397eeb8a9c771b8e42c4fb88420e676bddbd096854a3ad84f4d49e` |
| `docs/configuration.md` | `fc48d1ce7d885c5253222415fb7a4b46d75f80e6431eb924174657ff775099eb` |
| `docs/models-configuration.md` | `1761b36835cf37fbeb7adc9ebb1cb88d190145f69a6d507ad5d4349f17f908a6` |
| `docs/rpc.md` | `54e29df00e080c962df4fbb516dc441a9ce6805287bea61d45f3c99f92078446` |
| `docs/rpc-commands.md` | `4deeee367852f4633ef594bc079f9e9fb14640a4e3ba8a69b7c89b6f54740838` |

`getAgentDir` (`dist/config.js:510–516`) consumes
`PI_CODING_AGENT_DIR`. `modelFromJson`
(`dist/core/provider-composer.js:83–115`) produces model identity, provider,
API, base URL, independent reasoning, input modalities and limits. Its omitted
values default to false/128000/16384; this collector explicitly supplies and
checks the source-backed values instead.

`get_state` (`dist/modes/rpc/rpc-mode.js:367–382`) reports the actual
`session.model`, session id, active-state booleans and message/pending counts.
The RPC response repeats the caller id and command. The RPC input handler
(`:622–684`) accepts JSONL, reports failures and shuts down on stdin EOF.
`dist/main.js:782–790` does not consume stdin as a prompt in RPC mode. The
experiment sends only one `get_state` command, then closes stdin; no prompt,
model mutation or compaction command is sent.

The reviewed RPC experiment is version-bound to 1.5.2 for this source proof;
it does not add a production version gate. A later binary needs reviewed
source/collector updates, not a fabricated same-version artifact.

## Real inputs, composition and sink

`TestLivePiModelControls` constructs the direct isolated experiment using the
installed executable, explicit `ARBITRON_LUNAROUTE_E2E_PI_MODEL`, the actual
production capability loader (including an optional existing override path)
and the committed R2 snapshot. Before any process launch, selected context,
output and independent reasoning must exactly agree with that snapshot. A
missing fact, revoked alias, unknown reasoning or duplicate source record
refuses; effort-list membership never supplies reasoning.

A fresh owner-only experiment contains separate HOME, XDG directories,
workspace, temporary directory and relocated agent directory. Private user
settings/auth and project settings select foreign models. The relocated agent
contains a 0600 LR-08 `models.json`, including the env key reference and
`authHeader:true`. A synthetic credential is supplied in the child environment
only. No operator key, ambient provider credential, runtime-injection env,
proxy or shared auth/config is inherited. `PI_OFFLINE=1` suppresses background
catalog/update/package traffic; there is no inference request.

Bounded same-executable/env/cwd version and RPC queries feed the collector.
Exactly one successful response with the matching command/id must report the
selected canonical provider/model, exact hosted `/v1` base, chat API,
text-only input, independent reasoning and exact loaded limits. Required
booleans/counts must explicitly describe an empty idle session. Missing,
malformed, duplicate, oversized, active, substituted or unsafe evidence
refuses. Raw state, diagnostics, paths, config and credentials stay private.

Bounded byte/mode snapshots of the private user/project trees and relocated
model definition must remain identical. Successful private-directory cleanup
precedes the exclusive credential-safe JSON writer. The artifact retains actual
resolved fields, session/version, source references and scope
`rpc_resolved_model_only`. It is not accepted by the completed-backend
publisher as an inference/completion observation.

`TestPiModelControlsInstalledSchema` runs this same collector against all three
canonical models using the genuine R2 source and embedded defaults. These
inference-free installed checks passed on 1.5.2. Subprocess fixtures separately
prove refusal and the final private JSON sink; they are not installed gateway
observations. No repository observation is fabricated by ordinary tests.

## Still pending

This does not prove that subscription files were never read, full project
trust/isolation, auth interpolation/Bearer on a real request, the same emitted
JSON-mode mission's effective controls, enforced limits, exact effort,
compaction/helper lifecycle, or successful final tool/file/command work. The
full `TestLivePiLunaRoute` collector still needs those real producers. LR-08
remains decided, support remains unknown and release readiness stays closed.
