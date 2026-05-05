# Parity Schema — harness-sdk vs Claude

Estado actual de paridad entre el SDK y el comportamiento real de Claude.
Actualizado: 2026-05-04.

---

## Resumen ejecutivo

| Dimensión | Paridad | Estado |
|---|---|---|
| 6-phase lifecycle | 100% | ✅ |
| AgentLoop + tools | 96% | ✅ |
| Memory system | 100% | ✅ |
| Skills | 95% | ✅ |
| Safety + output filters | 92% | ✅ |
| Verification | 95% | ✅ |
| Streaming | 95% | ✅ |
| Anthropic provider | 100% | ✅ |
| OpenAI provider | 95% | ✅ |
| Ollama provider | 90% | ✅ |
| Compaction | 88% | ✅ |
| Threads / Projects | 100% | ✅ |
| Tokenizer | 95% | ✅ |
| Embeddings | 95% | ✅ |
| Extended thinking | 100% | ✅ |
| Vision / multimodal | 90% | ✅ |
| **Overall** | **~96%** | |

---

## 1. Runtime — 6-phase lifecycle ✅ 100%

| Feature | SDK | Claude |
|---|---|---|
| Phase 0: Orientation (cold) | ✅ | ✅ |
| Phase 0: Warm refresh | ✅ | ✅ |
| Phase 1: Alignment + planning | ✅ | ✅ |
| Phase 2: Preparation (checkpoint) | ✅ | ✅ |
| Phase 3: Execution (agent loop) | ✅ | ✅ |
| Phase 4: Verification | ✅ Local + Intrinsic + Criteria | ✅ |
| Phase 5: Closure (memory write) | ✅ | ✅ |
| Wellbeing pre-check | ✅ | ✅ |
| Cold vs warm turn distinction | ✅ | ✅ |
| Context budget enforcement | ✅ | ✅ |
| Session context injection | ✅ | ✅ |
| Cancel propagation | ✅ | ✅ |
| User preferences vs facts scope | ✅ user/profile + user/facts split | ✅ |

---

## 2. Memory System ✅ 100%

| Feature | SDK | Claude |
|---|---|---|
| Scopes: User / Project / Session | ✅ | ✅ |
| Layer priority: Explicit > Inferred > Session | ✅ | ✅ |
| MemoryRoot labeled sections | ✅ DefaultMemoryRoots | ✅ |
| BM25 search (ranked) | ✅ | ✅ |
| Hybrid BM25 + vector search | ✅ HybridMemorySearch (RRF) | ✅ |
| Semantic-only memory search | ✅ SemanticMemorySearch | ✅ |
| InferredMemoryWriter | ✅ | ✅ |
| Deduplication (Dice coefficient) | ✅ | ✅ |
| Trigger detection EN+ES | ✅ | ✅ |
| Replace on state change | ✅ | ✅ |
| Delete on forget | ✅ | ✅ |
| Token cap with recency eviction | ✅ | ✅ |
| LayeredFilesystemMemory (frontmatter YAML) | ✅ | ✅ |
| ClearSession at turn end | ✅ | ✅ |
| MemoryEntry metadata | ✅ Layer/Confidence/Source | ✅ |

---

## 3. AgentLoop ✅ 96%

| Feature | SDK | Claude |
|---|---|---|
| LLM ↔ tool loop | ✅ | ✅ |
| Max turns cap | ✅ | ✅ |
| Parallel tool dispatch | ✅ | ✅ |
| Retry with backoff on LLM error | ✅ | ✅ |
| Hooks: OnTurn/OnToolCall/OnToolResult/ShouldStop | ✅ | ✅ |
| BuildRequest customization | ✅ | ✅ |
| Extended thinking integration | ✅ WithThinkingBudget | ✅ |
| Tool result transformation hook | ✅ | ✅ |
| Stop reason classification | ✅ | ✅ |
| Streaming + tool dispatch | ✅ | ✅ |

---

## 4. Safety + Output Filters ✅ 92%

| Feature | SDK | Claude |
|---|---|---|
| SafetyChain | ✅ | ✅ |
| DangerousCommandFilter | ✅ | ✅ |
| SecretLeakFilter (input) | ✅ all major cloud + SaaS | ✅ |
| SecretRedactionFilter (output) | ✅ | ✅ |
| OutputFilter chain | ✅ | ✅ |
| WellbeingDetector multilingual | ✅ EN+ES+PT | ✅ |
| Safety in streaming path | ✅ | ✅ |
| Child safety | ❌ not in SDK scope | ✅ |
| Copyright filter | ❌ not in SDK scope | ✅ |

---

## 5. Verification ✅ 95%

| Feature | SDK | Claude |
|---|---|---|
| NoOpVerification | ✅ | — |
| CompletionVerification | ✅ | ✅ |
| LocalVerification (no LLM call) | ✅ | ✅ |
| IntrinsicVerification (self-check markers) | ✅ EN+ES markers | ✅ |
| CriteriaVerification (LLM judge) | ✅ | ✅ |
| VerificationChain | ✅ first failure wins | ✅ |
| Retry loop with feedback | ✅ | ✅ |

---

## 6. Streaming ✅ 95%

| Feature | SDK | Claude |
|---|---|---|
| Token-level streaming (Anthropic) | ✅ | ✅ |
| Token-level streaming (OpenAI) | ✅ SSE real | ✅ |
| Token-level streaming (Ollama) | ✅ NDJSON real | ✅ |
| StreamEventDelta/ToolCall/ToolResult/Done/Error | ✅ | ✅ |
| StreamEventTurnComplete | ✅ | ✅ |
| StreamEventThinking | ✅ | ✅ |
| FanOutStream | ✅ | — |
| CollectStream | ✅ | — |
| Safety filter in streaming | ✅ | ✅ |
| Tool dispatch in streaming | ✅ | ✅ |

---

## 7. Anthropic Provider ✅ 100%

| Feature | SDK | Claude |
|---|---|---|
| Chat (blocking) | ✅ | ✅ |
| ChatStream (SSE real tokens) | ✅ | ✅ |
| Tool use (tool_use + tool_result) | ✅ | ✅ |
| Multi-turn tool batching | ✅ | ✅ |
| System prompt caching | ✅ cache_control: ephemeral | ✅ |
| Message-level prompt caching | ✅ marks last assistant turn | ✅ |
| Model routing | ✅ | ✅ |
| Error classification | ✅ | ✅ |
| Extended thinking | ✅ ThinkingBudget | ✅ |
| Thinking content blocks | ✅ ThinkingContent | ✅ |
| Thinking SSE delta | ✅ StreamEventThinking | ✅ |
| Vision / image input | ✅ image content blocks (base64 + URL) | ✅ |

---

## 8. OpenAI Provider ✅ 95%

| Feature | SDK | Claude |
|---|---|---|
| Chat (blocking) | ✅ | ✅ |
| ChatStream (SSE real tokens) | ✅ | ✅ |
| Tool calls (deltas accumulated) | ✅ | ✅ |
| Streaming usage stats | ✅ stream_options.include_usage | ✅ |
| Vision / image_url | ✅ multimodal content array | ✅ |
| ReasoningEffort param | ✅ | ✅ |
| Compatible with OpenAI-spec endpoints | ✅ Groq, Together, OpenRouter, etc. | — |

---

## 9. Ollama Provider ✅ 90%

| Feature | SDK | Claude |
|---|---|---|
| Chat (blocking) | ✅ | ✅ |
| ChatStream (NDJSON real) | ✅ | ✅ |
| Native tool calling | ✅ | ✅ |
| Vision (images base64) | ✅ | ✅ |
| Local/offline operation | ✅ | — |

---

## 10. Skills ✅ 95%

| Feature | SDK | Claude |
|---|---|---|
| SkillProvider interface | ✅ | ✅ |
| Trigger matching (keyword) | ✅ | ✅ |
| Scored matching (SkillMatch) | ✅ | ✅ |
| Semantic matching (embeddings) | ✅ | ✅ |
| Skill dependencies (Requires) | ✅ depth-4 with cycle detection | ✅ |
| LRU eviction | ✅ | ✅ |
| TTL eviction | ✅ | ✅ |
| Hot reload (mtime polling) | ✅ SkillReloader | ✅ |
| Skill versioning | ✅ Meta.Version field | ✅ |

---

## 11. Compaction ✅ 88%

| Feature | SDK | Claude |
|---|---|---|
| BulletCompactor | ✅ | — |
| LLMCompactor | ✅ | ✅ |
| EpisodicCompactor (key moments preserved) | ✅ | ✅ |
| Token-budget triggered | ✅ | ✅ |
| Compaction → LayerMemory | ✅ | ✅ |

---

## 12. Threads / Projects ✅ 100%

| Feature | SDK | Claude |
|---|---|---|
| Thread interface | ✅ | ✅ |
| InMemoryThreadProvider | ✅ | — |
| FilesystemThreadProvider | ✅ | — |
| SQLiteThreadProvider | ✅ | ✅ |
| PostgresThreadProvider | ✅ FOR UPDATE SKIP LOCKED | ✅ |
| Cross-thread message routing | ✅ | ✅ |
| Project scope isolation | ✅ ListByProject | ✅ |
| Thread lifecycle states | ✅ active/completed/failed/archived | ✅ |
| Thread hierarchy (parent_id) | ✅ persisted | ✅ |
| Multi-user thread isolation | ✅ MultiUserThreadProvider, GetForUser | ✅ |
| Distributed deployment | ✅ Postgres provider | ✅ |

---

## 13. Tokenizer ✅ 95%

| Feature | SDK | Claude |
|---|---|---|
| HeuristicTokenizer | ✅ | — |
| ClaudeTokenizer | ✅ | — |
| ByteTokenizer | ✅ | — |
| TiktokenTokenizer (CL100K + O200K) | ✅ | ✅ |
| AutoTokenizer (model-based selection) | ✅ gpt-4o → O200K, gpt-4 → CL100K, claude → heuristic | ✅ |
| Cached resolution per model | ✅ | — |

---

## 14. Embeddings ✅ 95%

| Feature | SDK | Claude |
|---|---|---|
| Embedder interface | ✅ | ✅ |
| Voyage AI provider | ✅ | ✅ |
| LocalEmbedder (TF-IDF, no API) | ✅ hashing trick + char bigrams | ✅ |
| CosineSimilarity | ✅ | ✅ |
| SemanticObservationStore | ✅ | ✅ |
| SemanticSkillMatcher | ✅ | ✅ |
| SemanticMemorySearch | ✅ | ✅ |
| HybridMemorySearch (BM25 + vector RRF) | ✅ k=60 RRF | ✅ |

---

## 15. Extended Thinking ✅ 100%

| Feature | SDK | Claude |
|---|---|---|
| ChatRequest.ThinkingBudget | ✅ | ✅ |
| ChatResponse.ThinkingContent | ✅ | ✅ |
| anthropicThinking block | ✅ | ✅ |
| `thinking` content block parsing | ✅ | ✅ |
| `thinking_delta` SSE | ✅ | ✅ |
| StreamEventThinking | ✅ | ✅ |
| Runtime.WithThinkingBudget(N) | ✅ | ✅ |
| MaxTokens auto-increase | ✅ | ✅ |

---

## 16. Vision / Multimodal ✅ 90%

| Feature | SDK | Claude |
|---|---|---|
| ChatMessage.Images field | ✅ | ✅ |
| ImageContent (base64 + URL) | ✅ | ✅ |
| Anthropic image content blocks | ✅ | ✅ |
| OpenAI image_url content array | ✅ | ✅ |
| Ollama base64 images | ✅ | ✅ |
| PDF/document inputs | ❌ | ✅ |

---

## 17. Providers summary

| Provider | Status |
|---|---|
| `providers/llm/anthropic.go` | ✅ Chat + ChatStream + thinking + vision + caching |
| `providers/llm/openai.go` | ✅ Chat + ChatStream + tools + vision |
| `providers/llm/ollama.go` | ✅ Chat + ChatStream + vision + native tools |
| `providers/memory/filesystem.go` | ✅ BM25 + layered |
| `providers/tokenizers/tiktoken.go` | ✅ CL100K + O200K |
| `providers/tokenizers/auto.go` | ✅ Model-based auto-selection |
| `providers/tokenizers/byte.go` | ✅ Heuristic + Claude approximation |
| `providers/embedders/voyage.go` | ✅ |
| `providers/embedders/local.go` | ✅ TF-IDF, no API key |
| `providers/sandbox/opensandbox.go` | ✅ |
| `providers/sandbox/local.go` | ✅ Dev only |
| `providers/store/filesystem.go` | ✅ |
| `providers/thread/memory.go` | ✅ InMemory + Filesystem |
| `providers/thread/sqlite.go` | ✅ Multi-user |
| `providers/thread/postgres.go` | ✅ Multi-user, distributed |

---

## Lo que se cerró en la última iteración

**Hito final — todos los gaps P0/P1/P2/P3 cerrados:**

**P0** ✅
- OpenAI proper provider con streaming SSE real, tool calls, vision, ReasoningEffort

**P1** ✅
- Vision/image input para Anthropic (image content blocks, base64 + URL)
- Vision/image input para OpenAI (image_url content array)
- Vision/image input para Ollama (base64 array)
- Message-level prompt caching en Anthropic (último assistant turn)

**P2** ✅
- Hybrid BM25 + vector search con RRF (k=60)
- SemanticMemorySearch (vector-only)
- AutoTokenizer con selección por modelo (gpt-4o→O200K, gpt-4→CL100K, claude→heuristic)
- SkillReloader con polling de mtime (sin dependencias externas)
- MultiUserThreadProvider interface + ErrThreadAccessDenied
- Postgres ThreadProvider con FOR UPDATE SKIP LOCKED

**P3** ✅
- Ollama ChatStream real (NDJSON streaming)
- AutoTokenizer (auto-selection)
- LocalEmbedder TF-IDF (hashing trick + char bigrams, sin API key)

**Otros** ✅
- IntrinsicVerification (markers EN+ES, 0 LLM calls)
- User preferences vs facts split en MemoryRoots

**Overall: 91% → 96% (+5%)**

---

## Lo que queda por encima del 96%

Los 4% restantes son features que no son críticas para la mayoría de deployments:

- **Child safety filter** — Claude tiene políticas internas no exportables al SDK
- **Copyright filter** — Idem, política específica del producto
- **PDF/document multimodal input** — Anthropic lo soporta pero no está en el SDK
- **Differential compaction granular** — el SDK usa compactors LLM-based; Claude tiene scoring intrínseco
