package autobuild

// Engine is the central composition point that wires all providers together.
// Every provider is optional — if nil, the corresponding capability is
// simply unavailable. Consumers build an Engine via [New] and functional
// options.
//
// Compared to the original design, three changes were made:
//
//  1. WorkflowEngine + PlanProvider are replaced by ExecutionContext —
//     a single object that owns phase, plan, and todos together.
//
//  2. ModelRouter is removed as a separate field. If you need multi-model
//     routing, pass a RoutedLLMProvider as the LLM field — it implements
//     both LLMProvider and ModelRouter via duck typing.
//
//  3. ObservationStore and SystemPromptBuilder are added — they model
//     the session working memory and layered prompt assembly that were
//     previously implicit or missing entirely.
type Engine struct {
	Memory       MemoryProvider
	Sandbox      SandboxDriver
	Tools        *ToolRegistry
	Skills       SkillProvider
	Threads      ThreadProvider
	Checkpoints  CheckpointProvider
	Tasks        TaskProvider
	Modes        ModeProvider
	Events       EventBus
	LLM          LLMProvider         // primary LLM — use RoutedLLMProvider for multi-model
	Execution    ExecutionContext     // phase + plan + todos unified
	Observations ObservationStore    // session-scoped working memory
	Prompt       *SystemPromptBuilder // layered system prompt assembly
	Budget       *ContextBudget       // token budget across context layers
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
		Events:       NewEventBus(),
		Execution:    NewExecutionContext(),
		Observations: NewObservationStore(),
		Prompt:       NewSystemPromptBuilder(),
		Budget:       &budget,
	}
}

func (e *Engine) HasMemory() bool       { return e.Memory != nil }
func (e *Engine) HasSandbox() bool      { return e.Sandbox != nil }
func (e *Engine) HasTools() bool        { return e.Tools != nil }
func (e *Engine) HasSkills() bool       { return e.Skills != nil }
func (e *Engine) HasThreads() bool      { return e.Threads != nil }
func (e *Engine) HasCheckpoints() bool  { return e.Checkpoints != nil }
func (e *Engine) HasTasks() bool        { return e.Tasks != nil }
func (e *Engine) HasModes() bool        { return e.Modes != nil }
func (e *Engine) HasEvents() bool       { return e.Events != nil }
func (e *Engine) HasLLM() bool          { return e.LLM != nil }
func (e *Engine) HasExecution() bool    { return e.Execution != nil }
func (e *Engine) HasObservations() bool { return e.Observations != nil }
func (e *Engine) HasPrompt() bool       { return e.Prompt != nil }
func (e *Engine) HasBudget() bool       { return e.Budget != nil }
