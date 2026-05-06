# Backend Updates Required

Changes needed in `example/backend-chat/` to adopt all new SDK capabilities
added in the latest parity iteration. Ordered by priority.

---

## P0 — Replace inline OpenAI with SDK provider

**File:** `llm_factory.go`

The inline `openAIProvider` struct (lines 66–210) should be replaced with
`providers/llm.NewOpenAI` from the SDK. The SDK provider adds real SSE
streaming, correct multi-turn tool calls, vision support, and ReasoningEffort.

```go
// Before
import sdkllm "github.com/everfaz/autobuild-sdk/providers/llm"

routedProviders["openai"] = newOpenAIProvider(
    getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
    os.Getenv("OPENAI_API_KEY"),
)

// After
routedProviders["openai"] = sdkllm.NewOpenAI(
    getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
    os.Getenv("OPENAI_API_KEY"),
    getenv("BACKEND_MODEL", "gpt-4o"),
)
```

Then delete the `openAIProvider` struct and all its methods (lines 66–210).

---

## P0 — Replace claudeTokenizerAdapter with AutoTokenizer

**File:** `mode_provider.go`

The backend has a manual `claudeTokenizerAdapter` wrapper. Replace with the
SDK's `AutoTokenizer` which selects the correct tokenizer per model automatically
(gpt-4o → O200K, gpt-4 → CL100K, claude → heuristic).

```go
// Add import
import sdktokenizers "github.com/everfaz/autobuild-sdk/providers/tokenizers"

// In newModeEngineWithDB, replace:
WithTokenizer(&claudeTokenizerAdapter{}).

// With:
WithTokenizer(sdktokenizers.NewAutoForModel(model)).
```

Then delete `claudeTokenizerAdapter` struct and its `Count` method.

---

## P1 — Enable Extended Thinking for deep-work mode

**File:** `mode_provider.go`

The SDK now supports `WithThinkingBudget(N)`. Wire it for the `deep-work` mode
where users expect deeper reasoning.

```go
// In newModeEngineWithDB, after WithAutoApprovePlan:
if logContext.Mode == "deep-work" {
    runtime = runtime.WithThinkingBudget(10_000) // 10k thinking tokens
}
```

Add `THINKING_BUDGET` to `.env.example` (optional, per-mode override):

```bash
# Extended thinking budget (tokens). 0 = disabled. Min 1024 when enabled.
# Only used for deep-work mode.
THINKING_BUDGET=10000
```

---

## P1 — Wire Hybrid Memory Search

**File:** `memory_provider.go`

The SDK now has `HybridMemorySearch` (BM25 + vector via RRF) and
`SemanticMemorySearch`. If a Voyage API key is available, wrap the filesystem
provider with hybrid search for better memory retrieval.

```go
import (
    ab "github.com/everfaz/autobuild-sdk"
    sdkembedders "github.com/everfaz/autobuild-sdk/providers/embedders"
    sdkmemory "github.com/everfaz/autobuild-sdk/providers/memory"
)

func loadBackendMemory() (ab.MemoryProvider, []ab.MemoryRoot, error) {
    // ... existing code to create LayeredFilesystemMemory ...
    provider, err := sdkmemory.NewLayeredFilesystem(root)
    if err != nil {
        return nil, nil, err
    }

    // Wrap with Hybrid search if Voyage key available
    var mem ab.MemoryProvider = provider
    if apiKey := os.Getenv("VOYAGE_API_KEY"); apiKey != "" {
        embedder := sdkembedders.NewVoyage(apiKey, "voyage-3")
        mem = ab.NewHybridMemorySearch(provider, embedder)
        log.Printf("backend memory: hybrid BM25+vector search enabled")
    } else {
        // LocalEmbedder as fallback (no API key needed)
        embedder := sdkembedders.NewLocal(384)
        mem = ab.NewHybridMemorySearch(provider, embedder)
        log.Printf("backend memory: hybrid BM25+local-embedder search enabled")
    }

    return mem, ab.DefaultMemoryRoots, nil
}
```

Add to `.env.example`:

```bash
# Voyage AI — enables hybrid BM25+vector memory search
# Without this, falls back to LocalEmbedder (TF-IDF, no API key needed)
VOYAGE_API_KEY=
```

---

## P1 — Enable Skill Hot Reload

**File:** `mode_provider.go` or a new `skill_reload.go`

The SDK's `SkillReloader` watches the skills directory and reloads skills
when files change — useful in development and for runtime skill updates.

```go
import ab "github.com/everfaz/autobuild-sdk"

// After loadBackendSkills(), if you have a ReloadableSkillProvider:
if provider, ok := skills.(ab.ReloadableSkillProvider); ok {
    reloader := ab.NewSkillReloader("./skills", provider)
    reloader.SetPollInterval(10 * time.Second)
    reloader.SetOnReload(func(loaded, removed []string) {
        log.Printf("skills reloaded: +%v -%v", loaded, removed)
    })
    reloader.Start(ctx)
    // defer reloader.Stop() on shutdown
}
```

---

## P2 — Wire SQLite ThreadProvider

**File:** `main.go` or a new `thread_provider.go`

The SDK now has `SQLiteThreadProvider` with full multi-user isolation. Wire it
into the backend so threads/projects can be persisted and queried.

```go
import sdkthread "github.com/everfaz/autobuild-sdk/providers/thread"

// In main, after opening the DB:
threadProvider, err := sdkthread.OpenSQLite(db)
if err != nil {
    log.Fatalf("thread provider: %v", err)
}

// Wire into Engine:
engine.Threads = threadProvider

// Add REST endpoints:
// GET  /api/projects/:id/threads
// POST /api/projects/:id/threads
// GET  /api/threads/:id
// POST /api/threads/:id/messages
```

---

## P2 — Add IntrinsicVerification to chain

**File:** `mode_provider.go`

The new `IntrinsicVerification` checks for self-verification markers in the
response without an extra LLM call. Add it first in the chain.

```go
// Replace current VerificationChain:
WithVerification(ab.VerificationChain{Strategies: []ab.VerificationStrategy{
    ab.LocalVerification{MustNotBeEmpty: true, MinLength: 5, NoHallucination: true},
    ab.CompletionVerification{MinLength: 5},
}}).

// With:
WithVerification(ab.VerificationChain{Strategies: []ab.VerificationStrategy{
    ab.LocalVerification{MustNotBeEmpty: true, MinLength: 5, NoHallucination: true},
    ab.CompletionVerification{MinLength: 5},
    // IntrinsicVerification only for code-agent and deep-work modes:
    // ab.IntrinsicVerification{MinMarkers: 1},
}}).
```

---

## P3 — Enable Ollama streaming

**File:** `llm_factory.go`

The Ollama provider now supports real `ChatStream` (NDJSON). No code change
needed — `sdkllm.NewOllama` already exposes `ChatStream`. Just verify the
backend's stream handler calls `ChatStream` and not `Chat` when the provider
implements `StreamingLLMProvider`.

The runtime's `RunStream` already checks for `StreamingLLMProvider` — nothing
to wire. Verify with a curl test against a running Ollama instance.

---

## Summary checklist

| Update | File | Effort |
|---|---|---|
| ✅ Replace inline OpenAI with SDK provider | `llm_factory.go` | 30 min |
| ✅ Replace claudeTokenizerAdapter with AutoTokenizer | `mode_provider.go` | 5 min |
| ✅ WithThinkingBudget for deep-work mode | `mode_provider.go` | 10 min |
| ✅ HybridMemorySearch (Voyage or LocalEmbedder) | `memory_provider.go` | 20 min |
| ✅ SkillReloader for hot reload | `mode_provider.go` | 15 min |
| ✅ SQLiteThreadProvider + thread endpoints | `main.go` | 1 hour |
| ✅ IntrinsicVerification in chain | `mode_provider.go` | 5 min |
| ✅ Ollama streaming (verify only) | curl test | 10 min |

---

## ENV vars to add to `.env.example`

```bash
# Extended thinking (deep-work mode only)
THINKING_BUDGET=10000

# Voyage AI for hybrid memory search (optional — falls back to LocalEmbedder)
VOYAGE_API_KEY=

# Memory root (already exists)
BACKEND_MEMORY_ROOT=./memory
```
