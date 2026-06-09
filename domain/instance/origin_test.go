package instance

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestOriginFromContext_AbsentReturnsFalse(t *testing.T) {
	_, ok := OriginFromContext(context.Background())
	if ok {
		t.Fatalf("expected ok=false when no origin set")
	}
}

func TestWithOrigin_RoundTripsScheduleOrigin(t *testing.T) {
	id := uuid.New()
	ctx := WithOrigin(context.Background(), Origin{Source: OriginSchedule, ScheduleID: &id})
	got, ok := OriginFromContext(ctx)
	if !ok {
		t.Fatalf("expected ok=true after WithOrigin")
	}
	if got.Source != OriginSchedule {
		t.Fatalf("source = %q, want %q", got.Source, OriginSchedule)
	}
	if got.ScheduleID == nil || *got.ScheduleID != id {
		t.Fatalf("scheduleID = %v, want %v", got.ScheduleID, id)
	}
}

func TestWithOrigin_RunNowAndManualSources(t *testing.T) {
	ctx := WithOrigin(context.Background(), Origin{Source: OriginRunNow})
	got, ok := OriginFromContext(ctx)
	if !ok || got.Source != OriginRunNow {
		t.Fatalf("run_now: ok=%v source=%q", ok, got.Source)
	}
	if got.ScheduleID != nil {
		t.Fatalf("run_now with no schedule id should be nil, got %v", got.ScheduleID)
	}
}

func TestTriggerSourceConstants(t *testing.T) {
	if OriginManual != "manual" || OriginSchedule != "schedule" || OriginRunNow != "run_now" {
		t.Fatalf("unexpected source string values: %q %q %q", OriginManual, OriginSchedule, OriginRunNow)
	}
}

// Edge: a second WithOrigin on a context that already carries an origin
// overwrites the prior value (context.WithValue semantics). Documents that each
// trigger call gets its own origin and earlier origins cannot bleed through.
func TestWithOrigin_OverwritesPriorOrigin(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	ctx := WithOrigin(context.Background(), Origin{Source: OriginSchedule, ScheduleID: &id1})
	ctx = WithOrigin(ctx, Origin{Source: OriginRunNow, ScheduleID: &id2})
	got, ok := OriginFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Source != OriginRunNow {
		t.Errorf("source = %q, want run_now (second WithOrigin must win)", got.Source)
	}
	if got.ScheduleID == nil || *got.ScheduleID != id2 {
		t.Errorf("scheduleID = %v, want %v", got.ScheduleID, id2)
	}
}

func TestInstanceCarriesOriginFields(t *testing.T) {
	id := uuid.New()
	inst := Instance{TriggerSource: OriginSchedule, ScheduleID: &id}
	if inst.TriggerSource != OriginSchedule {
		t.Fatalf("Instance.TriggerSource not stored")
	}
	if inst.ScheduleID == nil || *inst.ScheduleID != id {
		t.Fatalf("Instance.ScheduleID not stored")
	}
}

func TestInstanceSummaryCarriesOriginFields(t *testing.T) {
	id := uuid.New()
	s := InstanceSummary{TriggerSource: OriginRunNow, ScheduleID: &id}
	if s.TriggerSource != OriginRunNow {
		t.Fatalf("InstanceSummary.TriggerSource not stored")
	}
	if s.ScheduleID == nil || *s.ScheduleID != id {
		t.Fatalf("InstanceSummary.ScheduleID not stored")
	}
}
