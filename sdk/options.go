package autobuild

// Option is a functional option for configuring an [Engine].
type Option func(*Engine)

// WithMemory sets the MemoryProvider on the engine.
func WithMemory(m MemoryProvider) Option {
	return func(e *Engine) { e.Memory = m }
}

// WithSandbox sets the SandboxDriver on the engine.
func WithSandbox(s SandboxDriver) Option {
	return func(e *Engine) { e.Sandbox = s }
}

// WithToolRegistry sets the ToolRegistry on the engine.
func WithToolRegistry(r *ToolRegistry) Option {
	return func(e *Engine) { e.Tools = r }
}

// WithSkills sets the SkillProvider on the engine.
func WithSkills(s SkillProvider) Option {
	return func(e *Engine) { e.Skills = s }
}

// WithThreads sets the ThreadProvider on the engine.
func WithThreads(t ThreadProvider) Option {
	return func(e *Engine) { e.Threads = t }
}

// WithCheckpoints sets the CheckpointProvider on the engine.
func WithCheckpoints(c CheckpointProvider) Option {
	return func(e *Engine) { e.Checkpoints = c }
}

// WithPlanning sets the PlanProvider on the engine.
func WithPlanning(p PlanProvider) Option {
	return func(e *Engine) { e.Plans = p }
}

// WithTasks sets the TaskProvider on the engine.
func WithTasks(t TaskProvider) Option {
	return func(e *Engine) { e.Tasks = t }
}

// WithModes sets the ModeProvider on the engine.
func WithModes(m ModeProvider) Option {
	return func(e *Engine) { e.Modes = m }
}

// WithWorkflow sets the WorkflowEngine on the engine.
func WithWorkflow(w WorkflowEngine) Option {
	return func(e *Engine) { e.Workflow = w }
}

// WithEventBus sets the EventBus on the engine.
func WithEventBus(b EventBus) Option {
	return func(e *Engine) { e.Events = b }
}

// WithLLM sets the primary LLMProvider on the engine.
func WithLLM(l LLMProvider) Option {
	return func(e *Engine) { e.LLM = l }
}

// WithRouter sets the ModelRouter for multi-model routing.
func WithRouter(r ModelRouter) Option {
	return func(e *Engine) { e.Router = r }
}
