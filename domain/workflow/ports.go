package workflow

// WorkflowRepository defines the port for loading and listing workflow definitions.
type WorkflowRepository interface {
	Load(name string) (*Workflow, error)
	List() ([]WorkflowSummary, error)
}
