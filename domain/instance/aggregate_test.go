package instance_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNodeExecutionAggregate_ZeroValue(t *testing.T) {
	var a instance.NodeExecutionAggregate
	assert.Equal(t, uuid.Nil, a.InstanceID)
	assert.Equal(t, "", a.LatestModel, "queued instance: no model")
	assert.Equal(t, 0, a.MaxAttempt, "queued instance: 0 retries")
	assert.True(t, a.FirstStarted.IsZero(), "queued instance: no start time")
}

func TestNodeExecutionAggregate_Populated(t *testing.T) {
	id := uuid.New()
	started := time.Now()
	a := instance.NodeExecutionAggregate{
		InstanceID:   id,
		LatestModel:  "gpt-4o",
		MaxAttempt:   3,
		FirstStarted: started,
	}
	assert.Equal(t, id, a.InstanceID)
	assert.Equal(t, "gpt-4o", a.LatestModel)
	assert.Equal(t, 3, a.MaxAttempt)
	assert.Equal(t, started, a.FirstStarted)
}
