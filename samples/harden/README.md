# harden

Stage workflow that mutation-tests a diff and strengthens tests until the
mutation score clears the gate. Model profiles are in `PROFILES.md`; the
per-language mutation adapters are `scripts/mutate-<lang>.sh`.

## Isolation: repository code never runs unisolated

harden runs code the repository controls: a project-local `stryker`, a dotnet
local tool, a dependency's postinstall, and above all the repository's own tests
(under every mutant). The script executor launches ordinary processes with the
service's environment, so without a boundary that code would hold the git
credentials configured for clone and push, the host HOME and every sibling run.

`scripts/sandbox.sh` is that boundary, and `detect.sh` (the version probes) and
every `scripts/mutate-<lang>.sh` adapter, including each `--survivors-only`
rescore, launch their tool only through it. Clone, push and manifest steps stay
outside it, so the credentials they need never enter it.

| | inside the sandbox |
|---|---|
| environment | `env -i`: the service's PATH, a scratch HOME and TMPDIR, a fixed locale/toolchain list, `CI=1`. No token, no `HOME`, no `CLAUDE_CONFIG_DIR`, no `SSH_AUTH_SOCK`. |
| filesystem | the toolchain (PATH, system directories, dependency caches such as `~/go/pkg/mod`, `~/.m2/repository`, `~/.cargo/registry`) read-only; this run's workspace readable; write access only to the adapter's own work directory (and the work tree for Go, whose tool mutates it in place). The host HOME, other users' homes, the workspace root and sibling runs cannot be read. `.git` of a writable work tree (and the common git dir of a linked worktree) is read-only, so a test cannot plant a hook that the trusted git calls would run. |
| network | loopback only: no registry, no cloud metadata service. On macOS .NET runs with `DOTNET_SYSTEM_NET_DISABLEIPV6=1` so `dotnet test` reaches its test host over IPv4 loopback (Seatbelt does not treat a v4-mapped `::ffff:127.0.0.1` as localhost). As a result, .NET tests that need IPv6 (binding `::1` or `IPv6Any`) are expected to FAIL on macOS hosts and pass on Linux, which gives a macOS-only red baseline. Seatbelt also counts the host's own interface addresses as localhost. .NET's named-mutex directory `/tmp/.dotnet` is writable on macOS. The runtime fixes that path, and Linux gets a private `/tmp`. Dependencies must already be in the caches or the repository (`go mod vendor`, a warm `~/.m2`, `node_modules`). |

**Enforcement.** bubblewrap on Linux (mount, pid and network namespaces),
`sandbox-exec` on macOS. **Fail closed:** with neither, or when the sandbox
cannot be built (unprivileged user namespaces disabled), `detect` exits 1 and an
adapter exits 1 before running anything, with `no isolation available`.
`sandbox.sh` exits 125 for that, so a tool's own failure is never mistaken for
it. There is no unisolated fallback.

- **Linux hosts:** install `bubblewrap` (`apk add bubblewrap`, `apt install
  bubblewrap`) and leave unprivileged user namespaces enabled
  (`kernel.unprivileged_userns_clone=1` where the distro has the knob). The
  shipped `systemd` unit needs no change. The container image includes
  `bubblewrap`; a container must be allowed to create user namespaces (Docker's
  default seccomp profile refuses; run with a profile that permits `unshare`, or
  `--privileged` for a trusted host).
- **macOS hosts: provider keys are readable from inside the sandbox.** macOS
  gives any process the exec-time environment of every non-Apple process of
  the same user through `sysctl(KERN_PROCARGS2)`, and no Seatbelt rule
  mediates it. `sandbox.sh` relaunches itself with a scrubbed environment, so
  neither the command nor its parent holds a token. But Arbitron injects
  provider credentials into the environment of its agent subprocesses on every
  call, by design (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, ... for claude,
  opencode and codex; see `adapter/procenv/procenv.go` and
  `adapter/opencode/providers.go`). Those processes often run while a harden
  run does, for example another instance's `agent_call`. On macOS, code from an
  untrusted repository under harden can read those keys, and the server's own
  environment too, if credentials sit there (`script.envpassthrough`).
  Mitigation: run harden on Linux (bubblewrap's pid namespace hides every other
  process), or on macOS as a dedicated user, or on a host that runs no
  concurrent agent work. harden does not refuse to run on macOS.
- **Go toolchains:** inside the sandbox Go runs with `GOTOOLCHAIN=local`, no proxy
  and no sum database, so it cannot switch toolchains itself. For a Go module
  (a `go.mod` or `go.work` at or above the command's directory), `sandbox.sh`
  runs `go env GOROOT` OUTSIDE the sandbox, in that directory and with the
  service's own Go settings (so `GOTOOLCHAIN=auto` can pick the cached or
  downloaded toolchain the module asks for). The command then runs that GOROOT
  read-only, first on PATH. If resolution fails, or the answer has no `bin/go`,
  nothing runs (exit 125). Go telemetry is off inside. That outside step never
  runs repository code: `go` is taken only from an ABSOLUTE PATH entry, never
  from inside the run's workspace, and runs with a PATH of absolute entries
  outside it. The resolved GOROOT must be absolute and outside the workspace,
  the scratch dir and every writable path. Two operator notes. The step EXECUTES
  cached toolchains (`$GOMODCACHE/golang.org/toolchain@.../bin/go`), and go
  does not re-verify an extracted toolchain, so never add the module cache or
  GOPATH to `HARDEN_SANDBOX_RW`. By default they are mounted read-only. And
  the resolution has no time limit: a module may ask for any newer published
  toolchain, which `GOTOOLCHAIN=auto` then downloads through the service's
  proxy. Set `GOTOOLCHAIN=local` (or a fixed version) in the service
  environment to forbid that.
- **Operator extensions, from the SERVICE environment only** (never from a
  workflow input or the repository): `HARDEN_SANDBOX_RO` / `HARDEN_SANDBOX_RW`
  (colon-separated extra read-only / read-write paths), `HARDEN_SANDBOX_PASS`
  (names of environment variables to pass through, for toolchain locators),
  `ARBITRON_HARDEN_NETWORK=allow` (lift the network restriction; only if the
  host's egress policy cannot be relied on and dependencies must be fetched). The
  script executor only forwards variables named in `script.envpassthrough`, so
  list them there.

## Known limitation: Stryker.NET runs in a plain clone

The Go, JS and Rust adapters run their tool inside `work_dir`, the linked
worktree that `@lib/init-workspace.sh` adds at `{{workspace}}/work` (a worktree
of the instance clone in `{{workspace}}/src`). The Python and Java adapters run
in a `git archive HEAD` copy under `{{workspace}}/harden/<lang>`, which needs no
git metadata. `scripts/mutate-csharp.sh` is the only adapter that needs a full
git clone, because Stryker.NET needs a real repository for `--since`.

**Why.** Stryker.NET resolves the repository through LibGit2Sharp, which diffs
`--since:<sha>` against the repository's MAIN working tree (`{{workspace}}/src`),
not the linked worktree. No changed file in the diff then matches a file mutated
under `{{workspace}}/work`, so every mutant came back `Ignored` with the reason
"Removed by since filter" and the gate passed on zero mutants (live smoke
481a889f: 17 mutants, all dropped, the planted survivor never seen). Verified on
dotnet-stryker 5.0.0.

**What the adapter does instead.** It clones `work_dir` into
`{{workspace}}/harden/csharp/clone`, checks out `work_dir`'s `HEAD` (detached),
and runs Stryker from each test project directory inside that clone, where the
repository and the working tree are the same, so `--since` scopes correctly.
Report paths are mapped relative to the clone root, which mirrors `work_dir`.

**What it costs.**

- An extra clone of the repository per run (`harden/csharp` is wiped and
  re-created each time), plus a fresh `dotnet restore` and build inside it. Large
  repositories pay that time and disk on every C# mutation pass, including each
  survivors-only rerun.
- Only COMMITTED state is mutated. The clone is made from `HEAD`, so nothing
  uncommitted or untracked in `work_dir` is seen. In practice this loses nothing:
  the adapter refuses to start (`worktree not clean`) when `work_dir` has
  uncommitted changes, so the committed `HEAD` is the whole tree. Files that git
  ignores (a restored `obj/`, `bin/`, local config) are not carried over either;
  the clone rebuilds them. Submodules are not initialized in the clone.
- The benefit side effect: `bin/` and `obj/` never appear in `work_dir`.

**Silent-pass guard.** If the diff has C# targets but every reported mutant was
dropped by `--since` (nothing in the diff was actually mutated), the adapter
warns on stderr and adds `"warning": "all mutants since-filtered"` to its JSON
output. The exit status is unchanged; the score gate decides what zero mutants
means.
