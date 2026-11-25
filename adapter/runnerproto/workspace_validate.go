package runnerproto

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// validateWorkspaceOp checks the payload, scoped workspace and canonical WorkDir.
func (d *Dispatch) validateWorkspaceOp() error {
	if d.Chat != nil || d.Confirmation != nil || d.Session != nil {
		return errors.New("process/agent dispatch carries no chat, confirmation or session payload")
	}
	if d.Workspace == nil {
		return errors.New("process/agent dispatch requires a workspace plan")
	}
	if err := d.Workspace.Validate(); err != nil {
		return err
	}
	switch d.Op {
	case "process":
		if d.Process == nil || d.Agent != nil {
			return errors.New("process dispatch carries exactly the process payload")
		}
		if d.Process.WorkDir != d.Workspace.WorkDir {
			return errors.New("process.workDir must equal workspace.workDir")
		}
		return d.Process.Validate()
	default: // agent
		if d.Agent == nil || d.Process != nil {
			return errors.New("agent dispatch carries exactly the agent payload")
		}
		if d.Agent.WorkDir != d.Workspace.WorkDir {
			return errors.New("agent.workDir must equal workspace.workDir")
		}
		return d.Agent.Validate()
	}
}
func cleanAbs(p string) bool { return p != "" && path.IsAbs(p) && path.Clean(p) == p }
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Validate checks a process descriptor. Scripts travel only as verified bundles.
func (p *WireProcess) Validate() error {
	switch {
	case p.Command == "":
		return errors.New("process.command is required")
	case p.TimeoutSecs < 0:
		return errors.New("process.timeoutSecs must not be negative")
	case !cleanAbs(p.WorkDir):
		return errors.New("process.workDir must be a clean absolute path")
	case p.Dir != "" && !cleanAbs(p.Dir):
		return errors.New("process.dir must be a clean absolute path")
	case len(p.Requires) > maxTools || len(p.Passthrough) > maxTools || len(p.NodeEnv) > maxTools:
		return fmt.Errorf("at most %d requires, passthrough names or env entries", maxTools)
	}
	switch p.Kind {
	case "script":
		e := p.BundleEntrypoint
		if !isHex64(p.BundleHash) || p.BundleToken == "" || e == "" || path.IsAbs(e) || path.Clean(e) != e || e == ".." || strings.HasPrefix(e, "../") {
			return errors.New("a script dispatch needs a sha256 bundle hash, a relative in-bundle entrypoint and a bundle token")
		}
	case "tool_call":
	default:
		return fmt.Errorf("process.kind %q is not script or tool_call", p.Kind)
	}
	seen := map[string]bool{}
	for _, r := range p.Requires {
		if r == "" || strings.ContainsAny(r, "/\\ \t\r\n") || seen[r] {
			return fmt.Errorf("process.requires %q must be a unique bare command name", r)
		}
		seen[r] = true
	}
	for _, n := range p.Passthrough {
		if n == "" || strings.ContainsAny(n, "= \t\r\n") {
			return fmt.Errorf("process.passthrough %q is not an environment variable name", n)
		}
	}
	for k := range p.NodeEnv {
		if k == "" || strings.ContainsAny(k, "= \t\r\n") {
			return fmt.Errorf("process.nodeEnv key %q is not an environment variable name", k)
		}
	}
	return nil
}

// Validate checks the secret-free agent descriptor.
func (a *WireAgent) Validate() error {
	switch {
	case a.Model == "":
		return errors.New("agent.model is required (exact model, never substituted)")
	case a.Prompt == "":
		return errors.New("agent.prompt is required")
	case !cleanAbs(a.WorkDir):
		return errors.New("agent.workDir must be a clean absolute path")
	case !remoteCredentialKinds[a.CredentialKind]:
		return fmt.Errorf("agent.credentialKind %q cannot run on a remote runner", a.CredentialKind)
	case a.Backend == "":
		return errors.New("agent.backend is required")
	case a.TimeoutSecs < 0:
		return errors.New("agent.timeoutSecs must not be negative")
	case len(a.ToolScope) > maxTools:
		return fmt.Errorf("at most %d tools", maxTools)
	}
	return nil
}

// Validate accepts any exit code; the server executor decides process failure.
func (p *WireProcessResult) Validate() error { return nil }
func (a *WireAgentResult) Validate() error {
	if len(a.DroppedTools) > maxTools || len(a.Steps) > maxHeld || len(a.ModelEvidence) > maxCapabilities {
		return errors.New("agent result exceeds its bounds")
	}
	for _, e := range a.ModelEvidence {
		if e.Model == "" || e.Source == "" {
			return errors.New("agent model evidence names a model and a source")
		}
	}
	return nil
}
