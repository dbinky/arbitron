package runnerproto

import (
	"fmt"

	"github.com/google/uuid"
)

// WireCopy is one workspace copy reported in hello.
type WireCopy struct {
	RunID          string `json:"runId"`
	HomeGeneration int64  `json:"homeGeneration"`
}

func (c *WireCopy) Validate() error {
	if _, err := uuid.Parse(c.RunID); err != nil {
		return fmt.Errorf("%w: copy runId", ErrMalformed)
	}
	if c.HomeGeneration < 0 {
		return fmt.Errorf("%w: copy homeGeneration", ErrMalformed)
	}
	return nil
}

// DeleteCopy orders deletion only at or below HomeGeneration.
type DeleteCopy struct {
	RunID          string `json:"runId"`
	HomeGeneration int64  `json:"homeGeneration"`
	Reason         string `json:"reason,omitempty"`
}

func (d *DeleteCopy) Validate() error {
	c := WireCopy{RunID: d.RunID, HomeGeneration: d.HomeGeneration}
	return c.Validate()
}
