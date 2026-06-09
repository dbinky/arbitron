package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/schedule"
)

// scheduleSentinels is the complete set of exported schedule sentinels that may
// reach the REST surface. Adding a new exported sentinel to domain/schedule MUST
// add it here AND to writeScheduleError, or this coverage test fails — that is
// the point: it replaces the brittle status classifier.
var scheduleSentinels = []struct {
	name string
	err  error
}{
	{"ErrNotFound", schedule.ErrNotFound},
	{"ErrExists", schedule.ErrExists},
	{"ErrInvalidCron", schedule.ErrInvalidCron},
	{"ErrInvalidTimezone", schedule.ErrInvalidTimezone},
	{"ErrEmptyWorkflow", schedule.ErrEmptyWorkflow},
	{"ErrForbidden", schedule.ErrForbidden},
	{"ErrNotAuthorizedWorkflow", schedule.ErrNotAuthorizedWorkflow},
	{"ErrDepartmentNotMember", schedule.ErrDepartmentNotMember},
	{"ErrQuotaExceeded", schedule.ErrQuotaExceeded},
	{"ErrIntervalTooFrequent", schedule.ErrIntervalTooFrequent},
}

// Every exported schedule sentinel must map to a NON-default REST status. The
// default (catch-all) category is 500; a correctly-classified sentinel yields a
// specific 4xx.
func TestWriteScheduleError_AllSentinelsNonDefault(t *testing.T) {
	for _, s := range scheduleSentinels {
		t.Run(s.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeScheduleError(rec, s.err)
			require.NotEqual(t, http.StatusInternalServerError, rec.Code,
				"sentinel %s fell through to the default 500 status", s.name)
			require.GreaterOrEqual(t, rec.Code, 400, "an error sentinel must map to a 4xx")
			require.Less(t, rec.Code, 500, "an error sentinel must not map to a 5xx")
		})
	}
}
