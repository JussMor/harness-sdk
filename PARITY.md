# Parity Schema — harness-sdk vs Claude

Estado actual de paridad entre el SDK y el comportamiento real de Claude.
Actualizado: 2026-05-04.

---

## Resumen ejecutivo

| Dimensión | Paridad | Estado |
|---|---|---|
| 6-phase lifecycle | 98% | ✅ |
| AgentLoop + tools | 92% | ✅ |
| Memory system | 100% | ✅ |
| Skills | 85% | ✅ |
| Safety + output filters | 92% | ✅ |
| Verification | 88% | ✅ |
| Streaming | 87% | ✅ |
| Anthropic provider | 96% | ✅ |
| Compaction | 88% | ✅ |
| Threads / Projects | 90% | ✅ |
| Tokenizer | 80% | ⚠️ |
| Embeddings | 60% | ⚠️ |
| Extended thinking | 100% | ✅ |
| **Overall** | **~91%** | |

---

## 1. Runtime — 6-phase lifecycle ✅ 98%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| Phase 0: Orientation (cold) | ✅ memory + skills + observations | ✅ | — |
| Phase 0: Warm refresh | ✅ observations only | ✅ | — |
| Phase 1: Alignment + planning | ✅ HeuristicPlanner + LLMPlanner | ✅ | — |
| Phase 2: Preparation (checkpoint) | ✅ auto before mutations | ✅ | — |
| Phase 3: Execution (agent loop) | ✅ LLM ↔ tool loop | ✅ | — |
| Phase 4: Verification | ✅ LocalVerification + CriteriaVerification | ✅ | — |
| Phase 5: Closure (memory write) | ✅ explicit + inferred + dedup | ✅ | — |
| Wellbeing pre-check | ✅ multilingual, high severity = intercept | ✅ | — |
| Cold vs warm turn distinction | ✅ conv.IsCold() | ✅ | — |
| Context budget enforcement | ✅ ContextBudget.Enforce() | ✅ | — |
| Session context injection | ✅ LocalTimeSessionContext | ✅ | — |
| Cancel propagation | ✅ ctx through all phases | ✅ | — |
| User preferences scope | ⚠️ user/profile reads flat | ✅ structured | Minor diff in path conventions |

**Gap restante:** Claude distingue "user preferences" (instrucciones activas) de "user facts" (conocimiento) internamente con distinto peso. El SDK los iguala en priority dentro de `ScopeUser`.

---

## 2. Memory System ✅ 100%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| Scopes: User / Project / Session | ✅ | ✅ | — |
| Layer priority: Explicit > Inferred > Session | ✅ memory_layer.go | ✅ | — |
| MemoryRoot labeled sections in LayerMemory | ✅ DefaultMemoryRoots | ✅ | — |
| BM25 search (ranked) | ✅ providers/memory/filesystem.go | ✅ | — |
| InferredMemoryWriter (LLM extracts facts) | ✅ closure.go | ✅ | — |
| Deduplication (Dice coefficient) | ✅ WriteWithDedup | ✅ | — |
| Trigger detection EN+ES | ✅ DefaultMemoryTriggerDetector | ✅ | — |
| Replace on state change ("I now work at") | ✅ handleMemoryTrigger | ✅ | — |
| Delete on forget ("forget about X") | ✅ FORGET: prefix | ✅ | — |
| Token cap with recency eviction | ✅ WithMaxMemoryTokens | ✅ | — |
| LayeredFilesystemMemory (frontmatter YAML) | ✅ | ✅ | — |
| ClearSession at turn end | ✅ Observations.Expire() | ✅ | — |
| MemoryEntry metadata (Layer, Confidence, Source) | ✅ | ✅ | — |

---

## 3. AgentLoop ✅ 92%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| LLM ↔ tool loop | ✅ RunAgentLoop | ✅ | — |
| Max turns cap | ✅ default 50 | ✅ | — |
| Parallel tool dispatch (AreIndependent) | ✅ DispatchParallel | ✅ | — |
| Retry with backoff on LLM error | ✅ OnError hook | ✅ | — |
| Hooks: OnTurn, OnToolCall, OnToolResult, ShouldStop | ✅ | ✅ | — |
| BuildRequest customization | ✅ | ✅ | — |
| ReasoningTrace per turn | ✅ | ✅ | — |
| Extended thinking integration | ✅ via WithThinkingBudget | ✅ | — |
| Tool result transformation hook | ✅ | ✅ | — |
| Stop reason classification | ✅ complete/max_turns/aborted/error | ✅ | — |

---

## 4. Safety + Output Filters ✅ 92%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| SafetyChain (stacked filters) | ✅ | ✅ | — |
| DangerousCommandFilter | ✅ rm -rf, dd, /dev/null variants | ✅ | Minor: no obfuscation variants (r\m) |
| SecretLeakFilter (input) | ✅ Anthropic, OpenAI, GitHub, AWS, GCP, Azure, Slack, Stripe, SSH keys | ✅ | — |
| SecretRedactionFilter (output) | ✅ | ✅ | — |
| SafetyTransform (rewrite args) | ✅ | ✅ | — |
| OutputFilter chain | ✅ OutputFilterChain | ✅ | — |
| MaxLengthFilter | ✅ | ✅ | — |
| DisclaimerFilter | ✅ | ✅ | — |
| WellbeingDetector multilingual | ✅ EN + ES + PT | ✅ | Fewer languages than Claude |
| Safety in streaming path | ✅ RunStream applies SafetyFilter | ✅ | — |
| Child safety | ❌ | ✅ | Not in SDK scope |
| Copyright filter | ❌ | ✅ | Not in SDK scope |

---

## 5. Verification ✅ 88%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| NoOpVerification | ✅ | — | — |
| CompletionVerification | ✅ stop_reason + MinLength | ✅ | — |
| LocalVerification (no LLM call) | ✅ MustContain/NotContain/NoHallucination | ✅ | — |
| CriteriaVerification (LLM judge) | ✅ | ✅ | Expensive: 1 extra LLM call |
| VerificationChain | ✅ first failure wins | ✅ | — |
| Retry loop with user message | ✅ conv.AppendUser(verdict.Reason) | ✅ | — |
| MaxVerifyRetry | ✅ default 2 | ✅ | — |
| Self-verification (model checks own output) | ⚠️ via CriteriaVerification | ✅ intrinsic | Claude's internal verification is implicit |

---

## 6. Streaming ✅ 87%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| Token-level streaming (Anthropic) | ✅ ChatStream SSE | ✅ | — |
| StreamEventDelta / ToolCall / ToolResult / Done / Error | ✅ | ✅ | — |
| StreamEventTurnComplete | ✅ | ✅ | — |
| StreamEventThinking (extended thinking) | ✅ thinking_delta | ✅ | — |
| FanOutStream (multiple consumers) | ✅ | — | — |
| CollectStream (blocking collect) | ✅ | — | — |
| Safety filter in streaming path | ✅ | ✅ | — |
| Tool dispatch in streaming | ✅ DispatchParallel | ✅ | — |
| Ollama / OpenAI real token streaming | ❌ sentence-chunking fallback | ✅ | Only Anthropic has real ChatStream |

---

## 7. Anthropic Provider ✅ 96%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| Chat (blocking) | ✅ | ✅ | — |
| ChatStream (SSE real tokens) | ✅ | ✅ | — |
| Tool use: tool_use + tool_result blocks | ✅ | ✅ | — |
| Multi-turn tool batching (consecutive tool_result) | ✅ fixed | ✅ | — |
| Prompt caching (cache_control: ephemeral) | ✅ system prompt | ✅ | Only system prompt cached; not messages |
| Model routing (claude-sonnet-4, opus, haiku) | ✅ RoutedLLMProvider | ✅ | — |
| Error classification (rate limit, auth, etc.) | ✅ | ✅ | — |
| Max tokens configuration | ✅ default 8192 | ✅ | — |
| Extended thinking (`thinking` block) | ✅ ThinkingBudget | ✅ | — |
| Thinking content blocks parsed | ✅ ThinkingContent | ✅ | — |
| Thinking SSE delta (thinking_delta) | ✅ StreamEventThinking | ✅ | — |
| Vision / image input | ❌ | ✅ | No image content block support |
| Message-level prompt caching | ❌ | ✅ | Only system is cached |

---

## 8. Skills ✅ 85%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| SkillProvider interface | ✅ | ✅ | — |
| Trigger matching (keyword) | ✅ | ✅ | — |
| Scored matching (SkillMatch) | ✅ | ✅ | — |
| Semantic matching (embeddings) | ✅ SemanticSkillMatcher | ✅ | Requires Voyage API key |
| Skill dependencies (Requires) | ✅ recursive depth-4 | ✅ | — |
| LRU eviction | ✅ | ✅ | — |
| TTL eviction | ✅ | ✅ | — |
| Hot reload (disk changes) | ❌ | ✅ | Loaded once, no inotify |
| Skill versioning | ❌ | ✅ | No version tracking |
| Skill composition (output → next skill) | ❌ | ✅ | Requires skill not composition |

---

## 9. Compaction ✅ 88%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| BulletCompactor (local) | ✅ | — | — |
| LLMCompactor (LLM summarize) | ✅ | ✅ | — |
| EpisodicCompactor (key moments preserved) | ✅ | ✅ | — |
| Token-budget-triggered compaction | ✅ ContextBudget.Enforce() | ✅ | — |
| Compaction injected into LayerMemory | ✅ | ✅ | — |
| Differential compaction (importance scoring) | ⚠️ LLM-based | ✅ intrinsic | Claude's internal weighting is more granular |

---

## 10. Threads / Projects ✅ 90%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| Thread interface (Create/Get/Archive/SendMessage) | ✅ | ✅ | — |
| InMemoryThreadProvider | ✅ | — | — |
| FilesystemThreadProvider | ✅ JSON per-thread | — | — |
| SQLiteThreadProvider | ✅ schema, inbox routing, project listing | ✅ | — |
| Cross-thread message routing | ✅ SendMessage + ReadInbox | ✅ | — |
| Project scope isolation | ✅ ListByProject by status | ✅ | — |
| Thread lifecycle states | ✅ active/completed/failed/archived | ✅ | — |
| Thread hierarchy (parent_id) | ✅ field exists, persisted | ✅ | — |
| Multi-user thread isolation | ❌ | ✅ | No auth/user scoping yet |
| Postgres provider for distributed | ❌ | — | Single-process deployments only |

---

## 11. Tokenizer ⚠️ 80%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| HeuristicTokenizer (chars/4) | ✅ default | — | ~25% error for non-English |
| ClaudeTokenizer (word heuristic) | ✅ | — | ~3% error for English |
| ByteTokenizer | ✅ | — | — |
| TiktokenTokenizer (CL100K exact BPE) | ✅ providers/tokenizers | ✅ | Requires tiktoken-go dep |
| TiktokenTokenizer (O200K) | ✅ | ✅ | — |
| Auto-selection by model | ❌ | ✅ | Manual WithTokenizer required |
| Used in context budget by default | ⚠️ HeuristicTokenizer default | ✅ | Must explicitly set tiktoken |

---

## 12. Embeddings ⚠️ 60%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| Embedder interface | ✅ | ✅ | — |
| Voyage AI provider | ✅ | ✅ | Requires API key |
| CosineSimilarity | ✅ | ✅ | — |
| SemanticObservationStore | ✅ | ✅ | Requires embedder |
| SemanticSkillMatcher | ✅ | ✅ | Requires embedder |
| SemanticMemorySearch | ❌ | ✅ | BM25 only, no vector search |
| Bundled local embedder (no API key) | ❌ | ✅ | No offline option |
| Hybrid BM25 + vector search | ❌ | ✅ | Not implemented |

---

## 13. Extended Thinking ✅ 100%

| Feature | SDK | Claude | Gap |
|---|---|---|---|
| ChatRequest.ThinkingBudget | ✅ | ✅ | — |
| ChatResponse.ThinkingContent | ✅ parsed from response | ✅ | — |
| anthropicThinking block (`type: enabled, budget_tokens`) | ✅ | ✅ | — |
| `thinking` content block parsing | ✅ parseAnthropicResponse | ✅ | — |
| `thinking_delta` SSE event parsing | ✅ readAnthropicSSE | ✅ | — |
| StreamEventThinking emission | ✅ | ✅ | — |
| Runtime.WithThinkingBudget(N) | ✅ injects via BuildRequest | ✅ | — |
| MaxTokens auto-increase (budget+4096) | ✅ | ✅ | — |
| Minimum 1024 token enforcement | ✅ | ✅ | — |

---

## 14. Providers summary

| Provider | Status | Notes |
|---|---|---|
| `providers/llm/anthropic.go` | ✅ Chat + ChatStream | Prompt caching, tool batching, extended thinking |
| `providers/llm/ollama.go` | ✅ Chat only | No streaming |
| `providers/llm/openai.go` | ⚠️ Inline in backend-chat | Should be a proper provider |
| `providers/memory/filesystem.go` | ✅ BM25 + layered | |
| `providers/tokenizers/tiktoken.go` | ✅ CL100K + O200K | |
| `providers/tokenizers/byte.go` | ✅ | |
| `providers/embedders/voyage.go` | ✅ | Requires API key |
| `providers/sandbox/opensandbox.go` | ✅ Full driver | |
| `providers/sandbox/local.go` | ✅ Dev only | |
| `providers/store/filesystem.go` | ✅ | |
| `providers/thread/memory.go` | ✅ InMemory + Filesystem | |
| `providers/thread/sqlite.go` | ✅ Schema + inbox + listing | |

---

## Top gaps por impacto en producción

| Priority | Gap | Impact | Effort |
|---|---|---|---|
| P0 | OpenAI as proper provider | Streaming + tools for OpenAI | Medium |
| P1 | Message-level prompt caching | Cost reduction for long conversations | Medium |
| P1 | Image input (vision) | Multimodal conversations | Medium |
| P2 | Hybrid BM25 + vector search | Better memory retrieval | High |
| P2 | Tiktoken as default tokenizer | Budget accuracy | Low |
| P2 | Skill hot reload | Dev experience | Low |
| P2 | Multi-user thread isolation | Multi-tenant deployments | Medium |
| P3 | Ollama ChatStream | Real streaming for local models | Medium |
| P3 | Auto tokenizer selection by model | UX | Low |
| P3 | Bundled local embedder | Offline semantic search | High |

---

## Lo que se cerró en la última iteración

**Hito 1 — Extended Thinking (100%)**
- `ChatRequest.ThinkingBudget` + `ChatResponse.ThinkingContent`
- `anthropicThinking` block en el request body
- Parser de `thinking` content blocks en respuestas blocking
- Parser de `thinking_delta` events en SSE streaming
- `StreamEventThinking` emitido en tiempo real
- `Runtime.WithThinkingBudget(N)` con MaxTokens auto-increase

**Hito 2 — SQLite ThreadProvider (90%)**
- `OpenSQLite(db)` y `OpenSQLiteFile(path)` constructors
- Schema con threads + thread_inbox tables
- Indexes: `idx_thread_inbox_to`, `idx_threads_project`
- `Create/Get/Archive/SendMessage/ReadInbox` completos
- `ListByProject(projectID, status)` para projects
- `UpdateStatus` para lifecycle transitions
- Thread hierarchy via `parent_id` persistido

**Memory system (100% — completado en hitos previos)**
- BM25 search en filesystem provider
- LayeredFilesystemMemory con frontmatter YAML
- MemoryRoots con labels para orientación estructurada
- `WriteWithDedup` con Dice coefficient
- Triggers EN+ES con replace/delete intent
- `ClearSession` al final de cada turn

**Overall: 88% → 91% (+3%)**
