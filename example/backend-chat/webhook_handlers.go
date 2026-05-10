package main

import (
	"net/http"
)

// Webhook dispatch was removed. These handlers return 501 Not Implemented.
// To re-enable outbound webhooks, implement a WebhookStore and dispatcher
// that does not depend on the SDK's removed WebhookDispatcher type.

func (a *BackendChatApp) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "webhook support removed", http.StatusNotImplemented)
}

func (a *BackendChatApp) handleWebhookByID(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "webhook support removed", http.StatusNotImplemented)
}
