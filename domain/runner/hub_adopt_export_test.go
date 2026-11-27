package runner

// HubAdoptWaiting reports whether an adopter is registered for f on h. It is
// the registration barrier for external hub adoption tests (P3 T10 Cycle 5).
func HubAdoptWaiting(h *Hub, f Fence) bool { return h.adopt.Waiting(f) }
