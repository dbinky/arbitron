package engine

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResourceFenceRef_Validate(t *testing.T) {
	valid := ResourceFenceRef{Key: "run:123", Owner: "builtin", Generation: 1, Token: uuid.New()}
	require.NoError(t, valid.Validate())
	for _, field := range []string{"key", "owner", "generation", "negative generation", "token"} {
		t.Run(field, func(t *testing.T) {
			f := valid
			switch field {
			case "key":
				f.Key = ""
			case "owner":
				f.Owner = ""
			case "generation":
				f.Generation = 0
			case "negative generation":
				f.Generation = -1
			case "token":
				f.Token = uuid.Nil
			}
			require.Error(t, f.Validate())
		})
	}
	require.Error(t, (ResourceFenceRef{}).Validate())
}
