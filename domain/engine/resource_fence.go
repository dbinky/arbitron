package engine

import (
	"errors"

	"github.com/google/uuid"
)

// ResourceFenceRef identifies one durable ownership generation of a resource.
// Authorization consumers must validate the reference before comparing it to
// the current lease; a valid shape alone does not confer ownership.
type ResourceFenceRef struct {
	Key        string
	Owner      string
	Generation int64
	Token      uuid.UUID
}

// Validate rejects incomplete evidence before it can be used for authorization.
func (f ResourceFenceRef) Validate() error {
	if f.Key == "" || f.Owner == "" || f.Generation < 1 || f.Token == uuid.Nil {
		return errors.New("resource fence requires key, owner, positive generation and nonzero token")
	}
	return nil
}
