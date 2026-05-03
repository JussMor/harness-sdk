package autobuild

// Option is a functional option for configuring an [Engine].
type Option func(*Engine)

func WithMemory(m MemoryProvider) Option       { return func(e *Engine) { e.Memory = m } }
func WithSandbox(s SandboxDriver) Option       { return func(e *Engine) { e.Sandbox = s } }
func WithToolRegistry(r *ToolRegistry) Option  { return func(e *Engine) { e.Tools = r } }
func WithSkills(s SkillProvider) Option        { return func(e *Engine) { e.Skills = s } }
func WithThreads(t ThreadProvider) Option      { return func(e *Engine) { e.Threads = t } }
func WithCheckpoints(c CheckpointProvider) Option { return func(e *Engine) { e.Checkpoints = c } }
func WithTasks(t TaskProvider) Option          { return func(e *Engine) { e.Tasks = t } }
func WithModes(m ModeProvider) Option          { return func(e *Engine) { e.Modes = m } }
func WithEventBus(b EventBus) Option           { return func(e *Engine) { e.Events = b } }
func WithLLM(l LLMProvider) Option             { return func(e *Engine) { e.LLM = l } }
func WithExecution(x ExecutionContext) Option  { return func(e *Engine) { e.Execution = x } }
func WithObservations(o ObservationStore) Option { return func(e *Engine) { e.Observations = o } }
func WithPrompt(p *SystemPromptBuilder) Option { return func(e *Engine) { e.Prompt = p } }
func WithBudget(b *ContextBudget) Option       { return func(e *Engine) { e.Budget = b } }

