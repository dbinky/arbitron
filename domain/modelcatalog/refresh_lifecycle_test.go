package modelcatalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshRequiresAnnouncerAndRejectsLateReplacement(t *testing.T) {
	f := newCandidateFixture(t)
	s := &RefreshService{Pipeline: f.svc.d.Pipeline, Store: faultStore{}, Gate: readGate{}, Publisher: struct{ Publisher }{}, Journal: emptyPublicationJournal{}}
	require.Error(t, s.SetAnnouncer(nil))
	require.NoError(t, s.SetAnnouncer(&announceSpy{}))
	require.NoError(t, s.Recover(context.Background()))
	require.Error(t, s.SetAnnouncer(&announceSpy{}))
	s.Announcer = nil
	assert.Error(t, s.validate())
}

func TestRefreshAnnouncementErrorIsClassifiedWithoutUndoingSuccess(t *testing.T) {
	s := &RefreshService{Announcer: failedAnnouncer{}}
	r := RefreshReport{ID: "committed"}
	s.announce(context.Background(), &r)
	assert.Equal(t, "announcement_error", r.AnnouncementError)
	assert.Equal(t, "committed", r.ID)
}

func TestRefreshPipelineRequiresAdvanceValidator(t *testing.T) {
	f := newCandidateFixture(t)
	d := f.svc.d.Pipeline.d
	d.AdvanceValidator = nil
	_, err := NewDiscoveryPipeline(d)
	assert.ErrorIs(t, err, errNilCollaborator)
}

type failedAnnouncer struct{}

func (failedAnnouncer) Announce(context.Context, RefreshReport) error { return errNilCollaborator }
