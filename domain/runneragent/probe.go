package runneragent

import "context"

// startProbe keeps fresh source checks off the session's heartbeat loop.
func (a *Agent) startProbe(ctx context.Context, sess Session, req ProbeRequest) {
	key := string(req.SourceKind) + "/" + req.CredentialRef
	reply := func(src Source) {
		_ = sess.Send(ctx, Outbound{Kind: KindProbeResult, RequestID: req.RequestID, HealthRevision: req.HealthRevision, ProbeResult: &src})
	}
	fresh, ok := a.deps.Sources.(FreshSourceProber)
	a.probeMu.Lock()
	if !ok || a.probing[key] {
		a.probeMu.Unlock()
		reason := "probe_in_flight"
		if !ok {
			reason = "fresh_probe_unsupported"
		}
		reply(Source{Kind: req.SourceKind, CredentialRef: req.CredentialRef, UnavailableReason: reason})
		return
	}
	a.probing[key] = true
	a.probeMu.Unlock()
	a.probeWG.Add(1)
	go func() {
		defer a.probeWG.Done()
		src, err := fresh.ProbeSource(ctx, req.SourceKind, req.CredentialRef)
		a.probeMu.Lock()
		delete(a.probing, key)
		a.probeMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			src = Source{Kind: req.SourceKind, CredentialRef: req.CredentialRef, UnavailableReason: "probe_failed"}
		}
		reply(src)
	}()
}
