package autobuild

// Engine is the central composition point that wires all providers together.
// Every provider is optional — if nil, the corresponding capability is
// simply unavailable. Consumers build an Engine via [New] and functional
// options.
type Engine struct {
	Memory      MemoryProvider
	Sandbox     SandboxDriver
	Tools       *ToolRegistry
	Skills      SkillProvider
	Threads     ThreadProvider
	Checkpoints CheckpointProvider
	Plans       PlanProvider
	Tasks       TaskProvider
	Modes       ModeProvider
	Workflow    WorkflowEngine
	Events      EventBus
	LLM         LLMProvider  // primary LLM for chat completions
	Router      ModelRouter  // optional multi-model routing
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

// HasMemory returns true if a MemoryProvider is wired.
func (e *Engine) HasMemory() bool { return e.Memory != nil }

// HasSandbox returns true if a SandboxDriver is wired.
func (e *Engine) HasSandbox() bool { return e.Sandbox != nil }

// HasTools returns true if a ToolRegistry is wired and non-empty.
func (e *Engine) HasTools() bool { return e.Tools != nil }

// HasSkills returns true if a SkillProvider is wired.
func (e *Engine) HasSkills() bool { return e.Skills != nil }

// HasThreads returns true if a ThreadProvider is wired.
func (e *Engine) HasThreads() bool { return e.Threads != nil }

// HasCheckpoints returns true if a CheckpointProvider is wired.
func (e *Engine) HasCheckpoints() bool { return e.Checkpoints != nil }

// HasPlans returns true if a PlanProvider is wired.
func (e *Engine) HasPlans() bool { return e.Plans != nil }

// HasTasks returns true if a TaskProvider is wired.
func (e *Engine) HasTasks() bool { return e.Tasks != nil }

// HasModes returns true if a ModeProvider is wired.
func (e *Engine) HasModes() bool { return e.Modes != nil }

// HasWorkflow returns true if a WorkflowEngine is wired.
func (e *Engine) HasWorkflow() bool { return e.Workflow != nil }

// HasEvents returns true if an EventBus is wired.
func (e *Engine) HasEvents() bool { return e.Events != nil }

// HasLLM returns true if an LLMProvider is wired.
func (e *Engine) HasLLM() bool { return e.LLM != nil }

// HasRouter returns true if a ModelRouter is wired.
func (e *Engine) HasRouter() bool { return e.Router != nil }
