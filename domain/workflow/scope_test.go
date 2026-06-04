package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestScope_Private(t *testing.T) {
	id := uuid.New()
	s := workflow.Private(id)
	assert.Equal(t, workflow.ScopePrivate, s.Kind)
	assert.Equal(t, id, s.OwnerID)
}

func TestScope_Shared(t *testing.T) {
	s := workflow.Shared()
	assert.Equal(t, workflow.ScopeShared, s.Kind)
	assert.Equal(t, uuid.Nil, s.OwnerID)
}

func TestScope_KindsDistinct(t *testing.T) {
	assert.NotEqual(t, workflow.ScopePrivate, workflow.ScopeShared)
}
