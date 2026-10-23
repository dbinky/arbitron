package workflow

// ConsumerMode is how a model consumer drives its model: one batch call/mission
// or an interactive (streamed, multi-turn) session. The values match the
// execution-mode vocabulary the catalog inventory uses.
type ConsumerMode string

const (
	ConsumerModeBatch       ConsumerMode = "batch"
	ConsumerModeInteractive ConsumerMode = "interactive"
)

// Profile kinds a consumer requires; the same strings ProfileChecker returns.
const (
	ConsumerKindChat  = "chat"
	ConsumerKindAgent = "agent"
)

// ConsumerSource distinguishes a node's own model_profile from the
// parallel_group conflict resolver, which validation reports with its own codes.
type ConsumerSource string

const (
	ConsumerSourceNode          ConsumerSource = "node"
	ConsumerSourceGroupResolver ConsumerSource = "group_resolver"
)

// ModelConsumer is one execution site that resolves a model profile alias at
// run time. Alias may be empty: a spawn node's preparation alias is optional,
// and the other node types report the missing alias at validation.
type ModelConsumer struct {
	NodeID   string
	NodeType NodeType
	Source   ConsumerSource
	Alias    string
	Mode     ConsumerMode
	// Kind is the profile kind the site requires (ConsumerKindChat/Agent).
	Kind string
	// NodeBackend is the node-level agent backend override (agent_call only);
	// the runtime precedence is node > profile > deployment default.
	NodeBackend string
}

// ModelConsumers is the single description of which workflow nodes consume a
// model profile, how, and with which required kind. Run pin capture, the
// execution-path inventory and author-time profile validation all range over
// it, so no execution path can be omitted from one and not the others. Any new
// NodeType must be classified here (model_consumers_test.go enforces it).
func ModelConsumers(wf *Workflow) []ModelConsumer {
	if wf == nil {
		return nil
	}
	var out []ModelConsumer
	for _, n := range wf.Nodes {
		c := ModelConsumer{NodeID: n.ID, NodeType: n.Type, Source: ConsumerSourceNode, Alias: n.Config.ModelProfile, Mode: ConsumerModeBatch}
		switch n.Type {
		case NodeTypeLLMCall:
			c.Kind = ConsumerKindChat
		case NodeTypeAgentCall:
			c.Kind, c.NodeBackend = ConsumerKindAgent, n.Config.Backend
		case NodeTypeInteractiveLLM:
			c.Kind, c.Mode = ConsumerKindChat, ConsumerModeInteractive
		case NodeTypeInteractiveAgent:
			c.Kind, c.Mode = ConsumerKindAgent, ConsumerModeInteractive
		case NodeTypeSpawnWorkflow:
			// The input-preparation call is tool-suppressed: chat only.
			c.Kind = ConsumerKindChat
		case NodeTypeParallelGroup:
			if r, ok := groupResolverConsumer(n); ok {
				out = append(out, r)
			}
			continue
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

// groupResolverConsumer describes a parallel_group's conflict resolver: an
// interactive agent session (GroupResolutionBridge, then human turns through
// the conversation handler) on config.group.resolverProfile.
func groupResolverConsumer(n Node) (ModelConsumer, bool) {
	if n.Type != NodeTypeParallelGroup || n.Config.Group == nil || n.Config.Group.ResolverProfile == "" {
		return ModelConsumer{}, false
	}
	return ModelConsumer{NodeID: n.ID, NodeType: n.Type, Source: ConsumerSourceGroupResolver,
		Alias: n.Config.Group.ResolverProfile, Mode: ConsumerModeInteractive, Kind: ConsumerKindAgent}, true
}
