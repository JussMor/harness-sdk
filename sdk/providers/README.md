# Providers — opt-in implementations

The SDK core (`sdk/`) is intentionally provider-agnostic — every external
dependency is an interface. This `providers/` directory contains optional,
production-grade implementations you can import and wire into an Engine.

## Why opt-in

Each provider lives in its own subpackage so:

1. **Zero dependency creep** — importing `autobuild` core stays light.
   Only the providers you use pull in their dependencies.
2. **Swappable** — when Anthropic changes their API or you switch from
   Voyage to OpenAI embeddings, you change one import line.
3. **Versioned independently** — a tokenizer fix doesn't force a SDK release.

## Directory layout

```
providers/
├── tokenizers/   — token-counting implementations
│   ├── heuristic.go     (re-export of HeuristicTokenizer for convenience)
│   ├── byte.go          (UTF-8 rune-aware, no model match but better than chars/4)
│   └── claude.go        (approximation tuned for Claude tokenizer)
│
├── embedders/    — vector embedding providers
│   ├── voyage.go        (Voyage AI — recommended for Anthropic stack)
│   └── openai.go        (OpenAI embeddings, e.g. text-embedding-3-small)
│
├── llm/          — chat completion backends
│   ├── anthropic.go     (Claude via api.anthropic.com)
│   ├── ollama.go        (local models via Ollama)
│   └── openai.go        (OpenAI-compatible APIs)
│
├── memory/       — MemoryProvider implementations
│   ├── filesystem.go    (markdown files on disk)
│   └── inmemory.go      (process-local for tests)
│
├── store/        — ConversationStore implementations
│   ├── sqlite.go        (single-file persistent store)
│   └── filesystem.go    (one JSON file per conversation)
│
└── sandbox/      — SandboxDriver implementations
    ├── nosandbox.go     (runs commands directly, NO isolation — dev only)
    └── docker.go        (run commands inside Docker containers)
```

## Usage pattern

```go
import (
    autobuild "github.com/everfaz/autobuild-sdk/sdk"
    "github.com/everfaz/autobuild-sdk/sdk/providers/llm"
    "github.com/everfaz/autobuild-sdk/sdk/providers/embedders"
    "github.com/everfaz/autobuild-sdk/sdk/providers/tokenizers"
)

engine := autobuild.NewWithDefaults(128_000)
engine.LLM = llm.NewAnthropic(apiKey, "claude-sonnet-4-20250514")

runtime := autobuild.NewRuntime(engine).
    WithTokenizer(tokenizers.NewClaude()).
    WithSessionContext(autobuild.LocalTimeSessionContext())

// Wrap observation store with semantic search
embedder := embedders.NewVoyage(voyageKey, "voyage-3")
engine.Observations = autobuild.NewSemanticObservationStore(embedder)
```

## Stability

- Subpackages here can change independently of the core. Treat them as
  semver-minor changes.
- Core SDK interfaces (`autobuild.Tokenizer`, `autobuild.Embedder`, etc.)
  are stable — that's the contract these providers implement.
