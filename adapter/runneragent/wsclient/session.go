package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func chatJobFromWire(c *runnerproto.WireChat) *runneragent.ChatJob {
	if c == nil {
		return nil
	}
	return &runneragent.ChatJob{
		Provider: c.Provider, EndpointFingerprint: c.EndpointFingerprint,
		Model: c.Model, SystemPrompt: c.SystemPrompt, Prompt: c.Prompt, MaxTokens: c.MaxTokens, Temperature: c.Temperature,
		OutputSchema: c.OutputSchema, SessionID: c.SessionID, RequireExecutedModel: c.RequireExecutedModel, Stream: c.Stream,
		CredentialKind: c.CredentialKind, Backend: c.Backend, ContextWindow: c.ContextWindow, Effort: c.Effort, Tools: c.Tools,
		WorkDir: c.WorkDir, ProfileKind: c.ProfileKind, NativeCheckpoint: c.NativeCheckpoint, SessionExportToken: c.SessionExportToken,
	}
}

func resourceFromWire(r *runnerproto.WireResource) *runneragent.ResourceJob {
	if r == nil {
		return nil
	}
	return &runneragent.ResourceJob{ID: r.ID, Kind: r.Kind, WorkDir: r.WorkDir, FenceKey: r.FenceKey, FenceOwner: r.FenceOwner,
		FenceGeneration: r.FenceGeneration, FenceToken: r.FenceToken, CheckpointRef: r.CheckpointRef}
}

func bundleMetaFromWire(b *runnerproto.WireSessionBundle) *runneragent.SessionBundleMeta {
	if b == nil {
		return nil
	}
	return &runneragent.SessionBundleMeta{Backend: b.Backend, SessionID: b.SessionID, HarnessVersion: b.HarnessVersion,
		OriginWorkDir: b.OriginWorkDir, Checksum: b.Checksum, Size: b.Size}
}

func bundleMetaToWire(b *runneragent.SessionBundleMeta) *runnerproto.WireSessionBundle {
	if b == nil {
		return nil
	}
	return &runnerproto.WireSessionBundle{Backend: b.Backend, SessionID: b.SessionID, HarnessVersion: b.HarnessVersion,
		OriginWorkDir: b.OriginWorkDir, Checksum: b.Checksum, Size: b.Size}
}

func sessionJobFromWire(s *runnerproto.WireSessionOp) *runneragent.SessionJob {
	if s == nil {
		return nil
	}
	return &runneragent.SessionJob{Backend: s.Backend, SessionID: s.SessionID, WorkDir: s.WorkDir,
		TransferToken: s.TransferToken, Bundle: bundleMetaFromWire(s.Bundle)}
}

func sessionResultToWire(r *runneragent.SessionResult) *runnerproto.WireSessionResult {
	if r == nil {
		return nil
	}
	return &runnerproto.WireSessionResult{SessionID: r.SessionID}
}
