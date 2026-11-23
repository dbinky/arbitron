package runner

// Unexported JobState literals used by the Phase-2 domain code (hub, dispatcher
// runner-lost branch). Contracts §8 fixes only the strings; the exported
// JobState constant names belong to P1 T6, so Phase-2 code uses these literals
// and stays correct whatever names P1 T6 chooses. All other vocabularies use the
// exported P1 T3 constants (LivenessOnline, CompatCompatible, AuthOK, OpChat, ...).
const (
	stJobQueued    JobState = "queued"
	stJobOffered   JobState = "offered"
	stJobRunning   JobState = "running"
	stJobCompleted JobState = "completed"
	stJobFailed    JobState = "failed"
	stJobFenced    JobState = "fenced"
)
