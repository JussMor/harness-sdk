package autobuild

// Engine is the central composition point that wires all providers together.
// Every provider is optional — if nil, the corresponding capability is
// simply unavailable. Consumers build an Engine via [New] and functional
// options.
type Engine struct {
	Memory  MemoryProvider
	Sandbox SandboxDriver
	Tools   *ToolRegistry
	Threads ThreadProvider
	Modes   ModeProvider
	LLM     LLMProvider          // primary LLM — use RoutedLLMProvider for multi-model
	Prompt  *SystemPromptBuilder // layered system prompt assembly
	Budget  *ContextBudget       // token budget across context layers
}

// New creates an Engine configured with the given options.
// With no options, all providers are nil (opt-in architecture).
func New(opts ...Option) *Engine {
	e := &Engine{}
	for _, o := range opts {
		o(e)
	}
	return e
}

// NewWithDefaults creates an Engine with sensible in-memory defaults wired.
// Use this for quick setup; replace individual providers for production.
func NewWithDefaults(windowSize int) *Engine {
	budget := DefaultContextBudget(windowSize)
	return &Engine{
		Prompt: NewSystemPromptBuilder(),
		Budget: &budget,
	}
}

func (e *Engine) HasMemory() bool  { return e.Memory != nil }
func (e *Engine) HasSandbox() bool { return e.Sandbox != nil }
func (e *Engine) HasTools() bool   { return e.Tools != nil }
func (e *Engine) HasThreads() bool { return e.Threads != nil }
func (e *Engine) HasModes() bool   { return e.Modes != nil }
func (e *Engine) HasLLM() bool     { return e.LLM != nil }
func (e *Engine) HasPrompt() bool  { return e.Prompt != nil }
func (e *Engine) HasBudget() bool  { return e.Budget != nil }
