# Codex batch exact-model execution evidence

Retrieved 2026-10-16. Installed binary: `codex-cli 0.163.0` (`codex --version`).
`codex exec --help` verified `-m/--model`, `--cd`, `--json`,
`--dangerously-bypass-approvals-and-sandbox`, and prompt-on-stdin when no
positional prompt is supplied. No model invocation, login, installation or
upgrade was performed. These checks do not claim a live successful probe.

Owner decision D4 in
`docs/designs/2026-09-29-latest-model-profiles-decisions.md` defines the proof:
the turn must complete and its own stored rollout's `turn_context.model` must
match the requested exact model. Stdout/request/argv copying is not proof.

## Pinned primary sources

Repository: <https://github.com/openai/codex>. Release tag `rust-v0.163.0`
(tag object `129b8ec7ee6f4511557a4856279f529025c25af4`) resolves to commit
`83086188ae0614bd882818ff1aa23f3194962012`. All paths below are relative to
<https://github.com/openai/codex/tree/83086188ae0614bd882818ff1aa23f3194962012>.
Full raw files were retrieved over HTTPS; SHA-256 hashes:

| Path | SHA-256 |
| --- | --- |
| `codex-rs/protocol/src/protocol.rs` | `8a586a54d3ce19e3eaa5410e08c6724b7554f9a70126b3eb309180e54da98655` |
| `codex-rs/rollout/src/recorder.rs` | `42a973710d6eb7e6731b3774cdf30dba89f56fd7685cdeac2857825c08b83612` |
| `codex-rs/exec/src/exec_events.rs` | `dafa872d7e86a099e56e28a329dcb9c03db90ed768c3b88cca8c91d46dc1d0e5` |
| `codex-rs/exec/src/event_processor_with_jsonl_output.rs` | `bc296541b385f9b4ca2c28cc638e25a8d6b1332b2cd67f5a03eaa97a3d612b19` |

Recorded schema excerpts (irrelevant fields omitted, not a live rollout):

```rust
// protocol.rs: SessionMeta, TurnContextItem
pub struct SessionMeta {
    pub session_id: SessionId,
    pub id: ThreadId,
    // ...
}
pub struct TurnContextItem {
    pub turn_id: Option<String>,
    // ...
    pub model: String,
    // ...
}
// recorder.rs: JsonlWriter serializes each record, appends '\n', writes and
// flushes. RolloutLineRef flattens the item alongside timestamp/ordinal.
```

`recorder.rs` resolves `<codex_home>/sessions/YYYY/MM/DD`; the normal fresh
filename contains the thread ID. Reverted/forked rollout formats are not guessed
by this reader; missing or ambiguous matches remain errors. The existing
interactive verifier supplies the same account-home resolution and bounded
streaming reader used by the batch path (256 MiB per file, 16 MiB per record).

`exec_events.rs` names `thread.started` with `thread_id`, `turn.completed` and
`turn.failed`. `event_processor_with_jsonl_output.rs` emits `turn.completed`
only for `TurnStatus::Completed`; failure emits `turn.failed`. Therefore a clean
process exit or an assistant item without completion cannot prove success.
Pinned interactive turns enforce this in `foldExecStream` before verifying the
rollout or publishing Done. Empty or incomplete fresh/resumed streams are refused
even when a new matching `turn_context` exists; the child is still drained and reaped.

Tests write reduced, schema-shaped rollout fixtures only at the process-exit
leaf and check the real launch's model, account environment, cwd and prompt.
The real-root test asserts durable run/node acceptance, not a request echo.
Malformed/truncated or contradictory rollout identities cannot be discarded to
retain an earlier successful proof. Model strings are compared exactly.
The synthetic `resume-mismatch-*.jsonl` fixtures prove that a completed foreign
thread is refused without echoing either thread ID in Chat or StreamChat errors.

## Scope

This slice enforces normal pinned **batch execution**, using the existing
production factory and shared interactive rollout verifier. The interactive
provider (`startTurn`) also verifies whenever its binding is pinned, and refuses
a request model other than the pinned one (D14), not only when the request flag
is set. Legacy unpinned
behavior is unchanged. It does not implement a tool-disabled discovery smoke
call, Codex account-cache candidate filtering, the multi-path probe router,
persisted discovery confirmation or refresh/root assembly. Those remain P3T4
and P3T6/P3T7 work; a normal fully-open agent execution is not a safe smoke probe.

## Account model list (usability probe only)

D4 also limits probes to `~/.codex/models_cache.json` `.models[].slug` (or
`$CODEX_HOME/models_cache.json`). `account_models.go:accountListsModel` reads it
from the same home the launch env selects, requires an exact slug, and refuses a
missing, oversized (> 4 MiB) or malformed list before any launch. Only the
`.models[].slug` shape named by D4 is relied on; no live cache was inspected.
Real (non-probe) turns are not filtered: their rollout evidence decides.
