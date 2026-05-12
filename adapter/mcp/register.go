package mcp

// RegisterAllTools registers all MCP tools (workflow, instance, trace, system,
// and streaming) with the given server.
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
	RegisterStreamingTools(srv)
}
