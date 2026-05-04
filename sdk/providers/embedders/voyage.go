// Package embedders provides production Embedder implementations for the
// autobuild SDK. Each subfile is one provider — import only what you use.
package embedders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// Voyage is an Embedder backed by Voyage AI's API
// (https://docs.voyageai.com). Recommended for stacks built on Anthropic
// since Voyage models pair well with Claude's text representations.
//
// Default model: "voyage-3" (1024 dims, optimized for general retrieval).
// Other notable models: "voyage-3-large" (2048d), "voyage-code-2" (1536d).
type Voyage struct {
	APIKey       string
	Model        string // default "voyage-3"
	BaseURL      string // default "https://api.voyageai.com/v1/embeddings"
	Client       *http.Client
	dimsCached   int
}

// NewVoyage creates a Voyage embedder with the given API key and model.
// Pass empty model to use voyage-3 (1024 dims).
func NewVoyage(apiKey, model string) *Voyage {
	if model == "" {
		model = "voyage-3"
	}
	return &Voyage{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: "https://api.voyageai.com/v1/embeddings",
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed implements autobuild.Embedder.
func (v *Voyage) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := v.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("voyage: no embedding returned")
	}
	return vecs[0], nil
}

// EmbedBatch implements autobuild.Embedder. Voyage allows up to 128 texts
// per request and 320K tokens combined.
func (v *Voyage) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if v.APIKey == "" {
		return nil, fmt.Errorf("voyage: APIKey is required")
	}
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(map[string]any{
		"input": texts,
		"model": v.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", v.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.APIKey)

	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voyage: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("voyage: %d %s: %s", resp.StatusCode, resp.Status, string(respBody))
	}

	var raw struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("voyage: parse: %w", err)
	}

	out := make([][]float32, len(raw.Data))
	for _, d := range raw.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	if len(out) > 0 && len(out[0]) > 0 {
		v.dimsCached = len(out[0])
	}
	return out, nil
}

// Dimensions returns the vector size. Determined empirically on first call.
// Returns 0 before the first Embed call. For known models we hardcode:
//   voyage-3:        1024
//   voyage-3-large:  2048
//   voyage-code-2:   1536
//   voyage-large-2:  1536
func (v *Voyage) Dimensions() int {
	if v.dimsCached > 0 {
		return v.dimsCached
	}
	switch v.Model {
	case "voyage-3":
		return 1024
	case "voyage-3-large":
		return 2048
	case "voyage-code-2", "voyage-large-2":
		return 1536
	default:
		return 0
	}
}

var _ autobuild.Embedder = (*Voyage)(nil)
