package agent

import "context"

// ProviderConfig contains credentials and endpoint data for a model provider.
type ProviderConfig struct {
	Name    string
	BaseURL string
	APIKey  string
}

// Provider is the local adapter interface used by backend-chat ai-sdk package.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
