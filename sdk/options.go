package autobuild

// Option is a functional option for configuring an [Engine].
type Option func(*Engine)

func WithMemory(m MemoryProvider) Option      { return func(e *Engine) { e.Memory = m } }
func WithSandbox(s SandboxDriver) Option      { return func(e *Engine) { e.Sandbox = s } }
func WithToolRegistry(r *ToolRegistry) Option { return func(e *Engine) { e.Tools = r } }
func WithThreads(t ThreadProvider) Option     { return func(e *Engine) { e.Threads = t } }
func WithModes(m ModeProvider) Option         { return func(e *Engine) { e.Modes = m } }
func WithLLM(l LLMProvider) Option            { return func(e *Engine) { e.LLM = l } }
func WithPrompt(p *SystemPromptBuilder) Option { return func(e *Engine) { e.Prompt = p } }
func WithBudget(b *ContextBudget) Option      { return func(e *Engine) { e.Budget = b } }
