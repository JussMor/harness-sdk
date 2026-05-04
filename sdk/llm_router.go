package autobuild

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RoutedLLMProvider dispatches chat requests to one of several providers based
// on the model reference format "provider/model".
type RoutedLLMProvider struct {
	defaultProvider string
	providers       map[string]LLMProvider
}

// NewRoutedLLMProvider creates a multi-provider LLM dispatcher.
func NewRoutedLLMProvider(defaultProvider string, providers map[string]LLMProvider) *RoutedLLMProvider {
	normalized := make(map[string]LLMProvider, len(providers))
	for name, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || provider == nil {
			continue
		}
		normalized[key] = provider
	}
	return &RoutedLLMProvider{
		defaultProvider: strings.ToLower(strings.TrimSpace(defaultProvider)),
		providers:       normalized,
	}
}

// ParseModelRef splits a routed model reference into provider and model parts.
func ParseModelRef(model string) (providerName string, modelName string) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", ""
	}
	if strings.Contains(trimmed, "/") {
		parts := strings.SplitN(trimmed, "/", 2)
		return strings.ToLower(parts[0]), parts[1]
	}
	return "", trimmed
}

func (r *RoutedLLMProvider) Route(model string) (LLMProvider, error) {
	providerName, _ := ParseModelRef(model)
	if providerName == "" {
		providerName = r.defaultProvider
	}
	provider, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not configured", providerName)
	}
	return provider, nil
}

func (r *RoutedLLMProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	providerName, modelName := ParseModelRef(req.Model)
	if providerName == "" {
		providerName = r.defaultProvider
	}
	provider, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not configured", providerName)
	}

	routedReq := req
	routedReq.Model = modelName
	return provider.Chat(ctx, routedReq)
}

func (r *RoutedLLMProvider) HasProvider(name string) bool {
	_, ok := r.providers[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func (r *RoutedLLMProvider) Providers() []string {
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *RoutedLLMProvider) DefaultProvider() string {
	return r.defaultProvider
}

// ChatStream implements StreamingLLMProvider. It routes the request to the
// configured provider for the model's "provider/model" prefix and, if that
// provider also implements StreamingLLMProvider, forwards its stream events.
//
// Returns an error if the routed provider does not support streaming, so
// the caller (e.g. Runtime.RunStream) can fall back gracefully.
func (r *RoutedLLMProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	providerName, modelName := ParseModelRef(req.Model)
	if providerName == "" {
		providerName = r.defaultProvider
	}
	provider, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not configured", providerName)
	}
	streamer, ok := provider.(StreamingLLMProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support streaming", providerName)
	}

	routedReq := req
	routedReq.Model = modelName
	return streamer.ChatStream(ctx, routedReq)
}

// Verify RoutedLLMProvider implements StreamingLLMProvider so Runtime.RunStream
// takes the real streaming path when at least one underlying provider streams.
var _ StreamingLLMProvider = (*RoutedLLMProvider)(nil)