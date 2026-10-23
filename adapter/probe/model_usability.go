package probe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

const (
	// usabilityCheckTimeout bounds one exact-model check (D3-D6).
	usabilityCheckTimeout = procenv.ProbeTimeout
	// usabilityMaxParallel is the most checks the router runs at once.
	usabilityMaxParallel = 4
)

// ErrUsabilityNotConfirmed is the secret-free failure a lane reports when the
// exact-model execution could not be verified. Its text never carries
// provider output, so evidence and logs cannot leak credentials.
var ErrUsabilityNotConfirmed = errors.New("probe: exact-model execution not confirmed")

// LaneResult is the proof a lane produced for one (path, model) check: the
// spelling it saw executed and a non-secret reference to that execution.
type LaneResult struct {
	NativeModel string
	ProofRef    string
}

// UsabilityLane is one execution lane's exact-model check: the same launch path,
// flags and evidence reader a real run uses, with tools disabled (or the D10
// restricted Codex posture). A lane returns
// an error whenever it could not verify that the requested model executed, and
// must honor its context.
type UsabilityLane interface {
	Supports(modelcatalog.ExecutionPath) bool
	Check(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error)
}

// UsabilityRouterDeps configures a UsabilityRouter. Zero Timeout and Clock take
// the production values.
type UsabilityRouterDeps struct {
	Lanes   []UsabilityLane
	Timeout time.Duration
	Clock   func() time.Time
}

// UsabilityRouter implements modelcatalog.UsabilityProbe by dispatching each
// (path, entry) check to the first lane that supports the path. It owns the
// guarantees no lane may weaken: path identity on the evidence, a per-check
// deadline, a parallelism cap, and unknown (never usable) for every failure,
// panic, timeout, unsupported path or incomplete proof.
type UsabilityRouter struct {
	lanes   []UsabilityLane
	timeout time.Duration
	clock   func() time.Time
	sem     chan struct{}
}

var _ modelcatalog.UsabilityProbe = (*UsabilityRouter)(nil)

// NewUsabilityRouter requires at least one lane.
func NewUsabilityRouter(d UsabilityRouterDeps) (*UsabilityRouter, error) {
	if len(d.Lanes) == 0 {
		return nil, errors.New("probe: usability router needs at least one lane")
	}
	for _, l := range d.Lanes {
		if l == nil {
			return nil, errors.New("probe: usability router lane is nil")
		}
	}
	r := &UsabilityRouter{lanes: append([]UsabilityLane(nil), d.Lanes...), timeout: d.Timeout, clock: d.Clock, sem: make(chan struct{}, usabilityMaxParallel)}
	if r.timeout <= 0 || r.timeout > usabilityCheckTimeout {
		r.timeout = usabilityCheckTimeout
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	return r, nil
}

// candidateSpelling is the model string a check executes: the fixed version
// when the catalog has one, otherwise the catalog's native id.
func candidateSpelling(e modelidentity.Entry) string {
	if e.SpecificModel != "" {
		return e.SpecificModel
	}
	return e.NativeID
}

// Check returns unknown evidence for anything but a verified execution. It
// errors only when the inputs lack identity or the caller's context ends.
func (r *UsabilityRouter) Check(ctx context.Context, path modelcatalog.ExecutionPath, entry modelidentity.Entry) (modelcatalog.UsabilityEvidence, error) {
	spelling := candidateSpelling(entry)
	switch {
	case path.Key == "" || path.ConfigRevision == "":
		return modelcatalog.UsabilityEvidence{}, errors.New("probe: usability check needs a path key and configuration revision")
	case entry.CanonicalID == "" || spelling == "":
		return modelcatalog.UsabilityEvidence{}, errors.New("probe: usability check needs a canonical id and a model spelling")
	}
	if err := ctx.Err(); err != nil {
		return modelcatalog.UsabilityEvidence{}, err
	}
	// Codex authenticates through its own account, ignoring the connection.
	if path.Backend != "codex" {
		for _, route := range []string{path.RouteProvider, path.Binding.Provider} {
			family := domprovider.Canonicalize(route)
			if family == domprovider.CanonicalAzure || family == domprovider.CanonicalBedrock {
				return modelcatalog.UsabilityEvidence{}, fmt.Errorf("%w: native usability routes are pinned-only", domprovider.ErrInvalidSelection)
			}
		}
	}
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return modelcatalog.UsabilityEvidence{}, ctx.Err()
	}

	ev := modelcatalog.UsabilityEvidence{
		CanonicalID: entry.CanonicalID, PathKey: path.Key, NativeModel: spelling,
		ConfigRevision: path.ConfigRevision, State: modelcatalog.UsabilityUnknown,
	}
	var lane UsabilityLane
	for _, l := range r.lanes {
		if l.Supports(path) {
			lane = l
			break
		}
	}
	if lane == nil {
		ev.CheckedAt = r.clock()
		return ev, nil
	}

	cctx, cancel := context.WithTimeout(procenv.WithProbeResponseLimit(ctx), r.timeout)
	defer cancel()
	res, err := runLane(cctx, lane, path, entry)
	if perr := ctx.Err(); perr != nil {
		return modelcatalog.UsabilityEvidence{}, perr
	}
	ev.CheckedAt = r.clock()
	if err != nil || cctx.Err() != nil || res.ProofRef == "" || res.NativeModel != spelling {
		return ev, nil
	}
	ev.State, ev.ProofRef = modelcatalog.UsabilityConfirmed, res.ProofRef
	return ev, nil
}

// runLane isolates a panicking lane: its value may embed credentials, so it is
// reduced to the secret-free sentinel.
func runLane(ctx context.Context, l UsabilityLane, p modelcatalog.ExecutionPath, e modelidentity.Entry) (res LaneResult, err error) {
	defer func() {
		if recover() != nil {
			res, err = LaneResult{}, ErrUsabilityNotConfirmed
		}
	}()
	return l.Check(ctx, p, e)
}

// chatUsabilityPrompt is a fixed, tool-free request; its answer is never used.
const chatUsabilityPrompt = "Reply with the single word: ok"

// chatLaneProviders are the chat routes whose adapters prove the executed model
// (RequireExecutedModel): the direct HTTP connections from the response, and the
// Claude Code subscription chat adapter (claude-cli) from the CLI's modelUsage
// report, the same evidence a pinned run is held to. Azure and
// Bedrock have no evidenced deployment-to-version mapping yet (B5), so they are
// absent and the router reports them unknown.
var chatLaneProviders = map[string]bool{
	domprovider.CanonicalAnthropicAPI: true, "openai": true, "openrouter": true,
	"litellm": true, "ollama": true, "ollama-cloud": true, claudeCLIChatProvider: true,
}

// claudeCLIChatProvider is the binding provider of a chat profile with no
// connection (adapter/provider.ProviderClaudeCLI): Claude Code on the account.
const claudeCLIChatProvider = "claude-cli"

// ChatLaneDeps are the real collaborators of a ChatUsabilityLane: the factory
// that builds the chat adapter real runs use, and the path inventory's
// WithBinding, which supplies the path's endpoint and lazy key for the model.
type ChatLaneDeps struct {
	Chats       engine.ChatProviderSelector
	WithBinding func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error)
	// WorkDir is the scratch directory the Claude Code subscription adapter runs
	// in (it refuses an empty one). It must lie under the adapter's workspace
	// root; the lane creates it on first use. Without it the claude-cli route is
	// unsupported and reports unknown, never usable. HTTP routes ignore it.
	WorkDir string
}

// ChatUsabilityLane checks a chat route (a direct HTTP connection or the Claude
// Code subscription adapter) by sending one bounded,
// tool-free request on the explicit candidate spelling through the production
// chat factory. A discovery candidate is not a run pin and need not match the
// fixed-version grammar. The explicit request requirement still verifies actual
// execution, and the response must name the requested model.
type ChatUsabilityLane struct{ d ChatLaneDeps }

var _ UsabilityLane = (*ChatUsabilityLane)(nil)

func NewChatUsabilityLane(d ChatLaneDeps) (*ChatUsabilityLane, error) {
	if d.Chats == nil || d.WithBinding == nil {
		return nil, errors.New("probe: chat usability lane needs a chat selector and a binding source")
	}
	return &ChatUsabilityLane{d: d}, nil
}

// Supports admits batch chat routes on an evidenced provider. Agent harness
// backends and interactive streams need their own execution proof.
func (l *ChatUsabilityLane) Supports(p modelcatalog.ExecutionPath) bool {
	prov := domprovider.Canonicalize(p.Binding.Provider)
	if prov == claudeCLIChatProvider && l.d.WorkDir == "" {
		return false
	}
	return p.Kind == string(domprovider.ProfileKindChat) && p.Mode == modelcatalog.ExecutionModeBatch &&
		p.Backend == "" && chatLaneProviders[prov]
}

func (l *ChatUsabilityLane) Check(ctx context.Context, p modelcatalog.ExecutionPath, e modelidentity.Entry) (LaneResult, error) {
	model := candidateSpelling(e)
	b, err := l.d.WithBinding(ctx, p, model)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: binding", ErrUsabilityNotConfirmed)
	}
	b.Model, b.Pinned, b.OpaquePinRoute = model, false, ""
	chat, err := l.d.Chats.ChatProviderFor(b)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: no adapter", ErrUsabilityNotConfirmed)
	}
	req := engine.ChatRequest{Model: model, Prompt: chatUsabilityPrompt, MaxTokens: 16, RequireExecutedModel: true}
	if domprovider.Canonicalize(b.Provider) == claudeCLIChatProvider {
		workDir, clean, err := probeWorkDir(l.d.WorkDir)
		if err != nil {
			return LaneResult{}, err
		}
		defer clean()
		req.WorkDir, req.SuppressTools = workDir, true
	}
	resp, err := chat.Chat(ctx, req)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: call failed", ErrUsabilityNotConfirmed)
	}
	if resp == nil || resp.Model != model {
		return LaneResult{}, fmt.Errorf("%w: executed model differs", ErrUsabilityNotConfirmed)
	}
	return confirmCurrentBinding(ctx, l.d.WithBinding, p, model, LaneResult{NativeModel: resp.Model, ProofRef: "chat-exec:" + domprovider.Canonicalize(b.Provider) + ":" + resp.Model})
}

// ExecutedModelProber is an agent adapter's restricted exact-model check: one
// bounded turn on its pinned binding in workDir, returning the model the
// harness's own record shows executed and the session or thread id the harness
// issued for that turn. An adapter without one cannot confirm; a RunAgent
// result that merely echoes the requested model is never proof.
type ExecutedModelProber interface {
	ProbeExecutedModel(ctx context.Context, prompt, workDir string) (model, sessionID string, err error)
}

// agentLaneBackends are the agent backends whose adapters implement
// ExecutedModelProber: claude-code (modelUsage), pi (assistant message_end
// provider/model), opencode and codex (the account's stored rollout
// turn_context). The opencode probe fails closed until its launch is isolated
// from inherited MCP servers, plugins and config (G46), so that route stays
// unknown. Others are reported unknown by the router until their own
// exact-model probe exists.
var agentLaneBackends = map[string]bool{"claude-code": true, "pi": true, "opencode": true, "codex": true}

// AgentLaneDeps are the real collaborators of an AgentUsabilityLane: the factory
// that builds the agent adapter real runs use, the path inventory's
// WithBinding, and the scratch directory the probe runs in, which must lie under
// the adapter's workspace root (the lane creates it on first use).
type AgentLaneDeps struct {
	Agents      engine.AgentProviderSelector
	WithBinding func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error)
	WorkDir     string
}

// AgentUsabilityLane checks a batch agent route by building the production
// adapter for the explicit candidate and running its restricted exact-model
// probe, confirming only when the harness reports the requested model ran.
type AgentUsabilityLane struct{ d AgentLaneDeps }

var _ UsabilityLane = (*AgentUsabilityLane)(nil)

func NewAgentUsabilityLane(d AgentLaneDeps) (*AgentUsabilityLane, error) {
	if d.Agents == nil || d.WithBinding == nil || d.WorkDir == "" {
		return nil, errors.New("probe: agent usability lane needs an agent selector, a binding source and a work dir")
	}
	return &AgentUsabilityLane{d: d}, nil
}

// Supports admits batch agent routes on a backend with an exact-model probe.
// Interactive agent streams need their own execution proof.
func (l *AgentUsabilityLane) Supports(p modelcatalog.ExecutionPath) bool {
	return p.Kind == string(domprovider.ProfileKindAgent) && p.Mode == modelcatalog.ExecutionModeBatch &&
		agentLaneBackends[p.Backend]
}

func (l *AgentUsabilityLane) Check(ctx context.Context, p modelcatalog.ExecutionPath, e modelidentity.Entry) (LaneResult, error) {
	model := candidateSpelling(e)
	b, err := l.d.WithBinding(ctx, p, model)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: binding", ErrUsabilityNotConfirmed)
	}
	b.Model, b.Pinned, b.OpaquePinRoute = model, false, ""
	agent, err := l.d.Agents.AgentProviderFor(b)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: no adapter", ErrUsabilityNotConfirmed)
	}
	prober, ok := agent.(ExecutedModelProber)
	if !ok {
		return LaneResult{}, fmt.Errorf("%w: no exact-model probe", ErrUsabilityNotConfirmed)
	}
	workDir, clean, err := probeWorkDir(l.d.WorkDir)
	if err != nil {
		return LaneResult{}, err
	}
	defer clean()
	executed, session, err := prober.ProbeExecutedModel(ctx, chatUsabilityPrompt, workDir)
	if err != nil {
		return LaneResult{}, fmt.Errorf("%w: call failed", ErrUsabilityNotConfirmed)
	}
	if executed != model {
		return LaneResult{}, fmt.Errorf("%w: executed model differs", ErrUsabilityNotConfirmed)
	}
	// D8: the proof names the harness's own turn, never only the request.
	if session == "" {
		return LaneResult{}, fmt.Errorf("%w: no harness session", ErrUsabilityNotConfirmed)
	}
	return confirmCurrentBinding(ctx, l.d.WithBinding, p, model, LaneResult{NativeModel: executed, ProofRef: "agent-exec:" + p.Backend + ":" + executed + ":" + session})
}

// Each check owns its scratch directory; parallel checks cannot share files or
// remove each other's workspace. The parent remains stable for path identity.
func probeWorkDir(root string) (string, func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, fmt.Errorf("%w: work dir", ErrUsabilityNotConfirmed)
	}
	dir, err := os.MkdirTemp(root, "check-")
	if err != nil {
		return "", nil, fmt.Errorf("%w: work dir", ErrUsabilityNotConfirmed)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// A configuration/account edit during I/O cannot label the old path confirmed.
// The same inventory binding producer validates prospective and saved routes.
func confirmCurrentBinding(ctx context.Context, binding func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error), p modelcatalog.ExecutionPath, model string, result LaneResult) (LaneResult, error) {
	if _, err := binding(ctx, p, model); err != nil {
		return LaneResult{}, fmt.Errorf("%w: configuration changed", ErrUsabilityNotConfirmed)
	}
	return result, nil
}
