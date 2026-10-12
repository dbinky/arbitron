package instance_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNodeExecutionAggregate_ZeroValue(t *testing.T) {
	var a instance.NodeExecutionAggregate
	assert.Equal(t, uuid.Nil, a.InstanceID)
	assert.Equal(t, 0, a.MaxAttempt, "queued instance: 0 retries")
}

func TestNodeExecutionAggregate_Populated(t *testing.T) {
	id := uuid.New()
	a := instance.NodeExecutionAggregate{
		InstanceID: id,
		MaxAttempt: 3,
	}
	assert.Equal(t, id, a.InstanceID)
	assert.Equal(t, 3, a.MaxAttempt)
}
