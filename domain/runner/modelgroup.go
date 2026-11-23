// domain/runner/modelgroup.go (cycle 1 stub — replaced in cycle 2)
package runner

import "github.com/arbitron/arbitron/domain/modelidentity"

type ModelGroup struct {
	Kind          CredentialKind
	ConnectionRef string
	Family        modelidentity.Family
}
