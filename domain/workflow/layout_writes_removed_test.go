package workflow_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Automatic arrangement replaces manual positioning on the port and service.
func TestRepository_HasNoSaveNodePositions(t *testing.T) {
	_, ok := reflect.TypeOf((*workflow.Repository)(nil)).Elem().MethodByName("SaveNodePositions")
	assert.False(t, ok, "workflow.Repository must not declare SaveNodePositions")
}

func TestService_HasNoSaveNodePositions(t *testing.T) {
	_, ok := reflect.TypeOf(&workflow.Service{}).MethodByName("SaveNodePositions")
	assert.False(t, ok, "workflow.Service must not expose SaveNodePositions")
}
