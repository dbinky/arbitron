package mcp

// RegisterAllTools registers all MCP tools (workflow, instance, trace, system,
// streaming, and interactive) with the given server.
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
	RegisterStreamingTools(srv)
	RegisterInteractiveTools(srv)
	RegisterConnectionTools(srv)
}
