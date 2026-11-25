package runneragent_test

import (
	"crypto/ed25519"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/sources"
	"github.com/arbitron/arbitron/domain/runneragent"
)

type revisionSources struct {
	runneragent.LocalSources
	revision atomic.Uint64
}

func (s *revisionSources) Revision() uint64 { return s.revision.Load() }
func (s *revisionSources) HarnessVersions() map[string]string {
	return map[string]string{"codex": "0.171.0"}
}

func TestInventoryRevision_ReconnectsWithLiveHarnessEvidence(t *testing.T) {
	h := newHarness(t, 64)
	src := &revisionSources{LocalSources: h.src}
	var err error
	h.agent, err = runneragent.New(testConfig(), runneragent.Deps{Conn: h.conn, Exec: h.exec, Sources: src, Telemetry: h.tel, Spool: h.spool, Identity: fakeIdentity{key: h.key}, Clock: h.clock})
	require.NoError(t, err)
	h.start()
	mark := h.clock.mark()
	s := h.session()
	hello := admit(t, s, h.key.Public().(ed25519.PublicKey))
	require.Equal(t, map[string]string{"codex": "0.171.0"}, hello.HarnessVersions)
	src.revision.Add(1)
	h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	h.admitted()
	require.Equal(t, 2, h.conn.dialCount(), "source changes reconnect without a backoff timer")
}

// Compile-time: the refreshing inventory is what the agent watches.
func TestInventoryRevision_IsImplementedByTheSourceInventory(t *testing.T) {
	var _ runneragent.InventoryRevision = (*sources.Inventory)(nil)
	var _ runneragent.HarnessReporter = (*sources.Inventory)(nil)
}
