package runner

import (
	"context"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestObserveOutcome_DeliversToInstalledObserver(t *testing.T) {
	var got []SourceOutcome
	ctx := ContextWithOutcomeObserver(context.Background(), func(o SourceOutcome) { got = append(got, o) })
	h := 0.15
	ObserveOutcome(ctx, SourceOutcome{HeadroomHint: &h})
	assert.Len(t, got, 1)
	assert.Equal(t, 0.15, *got[0].HeadroomHint)
}
func TestObserveOutcome_NoObserverIsNoop(t *testing.T) {
	assert.NotPanics(t, func() { ObserveOutcome(context.Background(), SourceOutcome{}) })
	assert.NotPanics(t, func() { ObserveOutcome(ContextWithOutcomeObserver(context.Background(), nil), SourceOutcome{}) })
}
