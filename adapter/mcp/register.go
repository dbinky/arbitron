package mcp

// RegisterAllTools registers all MCP tools (workflow, instance, trace, system,
// streaming, interactive, and layout retry) with the given server.
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
	RegisterModelIdentityPolicyTools(srv)
	RegisterModelCatalogTools(srv)
	RegisterStreamingTools(srv)
	RegisterInteractiveTools(srv)
	RegisterConnectionTools(srv)
	RegisterProfileTools(srv)
	RegisterTokenTools(srv)
	RegisterUserTools(srv)
	RegisterLayoutTools(srv)
	RegisterScheduleTools(srv)
}
