package main

import (
	"net/http"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
)

// routerDeps is the production httpDeps projection with a test's own MCP and
// dashboard handlers; a nil notification or webhook handler keeps httpDeps'.
func (ds *domainStack) routerDeps(mcpHandler, dashHandler http.Handler, notifHandler *adapthttp.NotificationHandler, webhookHandler *adapthttp.WebhookHandler) adapthttp.RouterDeps {
	deps := ds.httpDeps()
	deps.MCPHandler, deps.DashboardHandler = mcpHandler, dashHandler
	if notifHandler != nil {
		deps.NotifHandler = notifHandler
	}
	if webhookHandler != nil {
		deps.WebhookHandler = webhookHandler
	}
	return deps
}
