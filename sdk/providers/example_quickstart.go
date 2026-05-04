// Package providers contains opt-in, production-grade implementations of the
// autobuild SDK interfaces. This file shows how to wire everything together.
package providers

// QuickStart example (not compiled — shows the wiring pattern).
//
//	import (
//	    autobuild "github.com/everfaz/autobuild-sdk"
//	    "github.com/everfaz/autobuild-sdk/sdk/providers/embedders"
//	    "github.com/everfaz/autobuild-sdk/sdk/providers/llm"
//	    "github.com/everfaz/autobuild-sdk/sdk/providers/memory"
//	    "github.com/everfaz/autobuild-sdk/sdk/providers/sandbox"
//	    "github.com/everfaz/autobuild-sdk/sdk/providers/store"
//	    "github.com/everfaz/autobuild-sdk/sdk/providers/tokenizers"
//	)
//
//	func BuildRuntime(cfg Config) *autobuild.Runtime {
//	    engine := autobuild.NewWithDefaults(128_000)
//
//	    // LLM
//	    engine.LLM = llm.NewAnthropic(cfg.AnthropicKey, "claude-sonnet-4-20250514")
//
//	    // Memory (files on disk)
//	    mem, _ := memory.NewFilesystem(cfg.MemoryRoot)
//	    engine.Memory = mem
//
//	    // Sandbox (local, dev-only — use DockerSandbox in production)
//	    engine.Sandbox = sandbox.NewLocal()
//
//	    // Embeddings + semantic search
//	    embedder := embedders.NewVoyage(cfg.VoyageKey, "voyage-3")
//	    engine.Observations = autobuild.NewSemanticObservationStore(embedder)
//
//	    // Session context
//	    sessionCtx := autobuild.StaticSessionContext(autobuild.SessionContext{
//	        UserName: cfg.UserName,
//	        Timezone: cfg.Timezone,
//	    })
//
//	    runtime := autobuild.NewRuntime(engine).
//	        WithMode("balanced").
//	        WithTokenizer(tokenizers.NewClaude()).
//	        WithConversationStore(func() autobuild.ConversationStore {
//	            s, _ := store.NewFilesystem(cfg.StoreRoot)
//	            return s
//	        }()).
//	        WithSessionContext(sessionCtx).
//	        WithSafety(autobuild.NewSafetyChain(
//	            autobuild.DefaultDangerousCommandFilter(),
//	            autobuild.DefaultSecretLeakFilter(),
//	        )).
//	        WithOutputFilter(autobuild.NewOutputFilterChain(
//	            autobuild.DefaultSecretRedactionFilter(),
//	        )).
//	        WithVerification(autobuild.CompletionVerification{MinLength: 10}).
//	        WithCompactor(&autobuild.BulletCompactor{MaxChars: 600}).
//	        WithPlanner(autobuild.DefaultHeuristicPlanner()).
//	        WithAutoApprovePlan(true)
//
//	    engine.Prompt.Set(autobuild.LayerCore, `You are a helpful engineering assistant.`)
//	    engine.Prompt.Set(autobuild.LayerBehavior, autobuild.DefaultBehaviorPrompt)
//
//	    return runtime
//	}
//
//	func Run(runtime *autobuild.Runtime, conversationID, message string) (string, error) {
//	    ctx := context.Background()
//
//	    // Load or create conversation
//	    conv, _ := runtime.Engine().Conversations.Load(ctx, conversationID)
//	    if conv == nil {
//	        conv = autobuild.NewConversation(conversationID)
//	    }
//
//	    result, err := runtime.Run(ctx, conv, message)
//	    if err != nil {
//	        return "", err
//	    }
//	    return result.Response, nil
//	}
