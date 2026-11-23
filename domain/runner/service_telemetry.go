package runner

// TelemetryWindow supplies a runner's recent samples, oldest first.
// Hub fills this window from heartbeats and the built-in telemetry loop.
type TelemetryWindow interface {
	Window(id RunnerID) []Telemetry
}

var _ TelemetryWindow = (*Hub)(nil)

// recentTelemetry returns an owned sample slice. No samples means unknown,
// represented by an empty, non-nil list rather than zero-valued metrics.
func (s *Service) recentTelemetry(id RunnerID) []Telemetry {
	if s.d.Window == nil {
		return []Telemetry{}
	}
	w := s.d.Window.Window(id)
	out := make([]Telemetry, len(w))
	copy(out, w)
	return out
}
