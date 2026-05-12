package mcp

// RegisterAllTools registers all MCP tools (workflow, instance, trace, and system)
// with the given server.
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
}
