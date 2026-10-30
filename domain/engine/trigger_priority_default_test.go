package engine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A trigger that omits priority (MCP instance_trigger without the argument)
// must create a "normal" run: the instances table CHECKs priority against
// high/normal/low, so an empty string failed the insert.
func TestEngine_TriggerDefaultsEmptyPriorityToNormal(t *testing.T) {
	for name, trigger := range map[string]func(*testFixtures) (uuid.UUID, error){
		"Trigger": func(f *testFixtures) (uuid.UUID, error) {
			id, _, err := f.engine.Trigger(context.Background(), "origin-test-wf", nil, "")
			return id, err
		},
		"TriggerWithDepartment": func(f *testFixtures) (uuid.UUID, error) {
			id, _, err := f.engine.TriggerWithDepartment(context.Background(), "origin-test-wf", nil, "", "")
			return id, err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTestFixtures()
			f.workflowRepo.add(originWF())
			id, err := trigger(f)
			require.NoError(t, err)
			inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
			assert.Equal(t, instance.PriorityNormal, inst.Priority)
		})
	}
}

// A supplied priority is kept exactly; the default applies only when absent.
func TestEngine_TriggerKeepsSuppliedPriority(t *testing.T) {
	for _, p := range []instance.Priority{instance.PriorityHigh, instance.PriorityNormal, instance.PriorityLow} {
		t.Run(string(p), func(t *testing.T) {
			f := newTestFixtures()
			f.workflowRepo.add(originWF())
			id, _, err := f.engine.Trigger(context.Background(), "origin-test-wf", nil, string(p))
			require.NoError(t, err)
			inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
			assert.Equal(t, p, inst.Priority)
		})
	}
}

// A whitespace-only priority is as absent as an empty one: it must default to
// normal rather than reach the instances CHECK.
func TestEngine_TriggerDefaultsWhitespacePriorityToNormal(t *testing.T) {
	for _, p := range []string{" ", "  ", "\t", " \n "} {
		t.Run(fmt.Sprintf("%q", p), func(t *testing.T) {
			f := newTestFixtures()
			f.workflowRepo.add(originWF())
			id, _, err := f.engine.Trigger(context.Background(), "origin-test-wf", nil, p)
			require.NoError(t, err)
			inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
			assert.Equal(t, instance.PriorityNormal, inst.Priority)
		})
	}
}
