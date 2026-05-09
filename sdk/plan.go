package autobuild

// Plan was removed. The DAG-of-executables model (Planned→Queued→InProgress→
// Completed state machine) was an artificial structure with no equivalent in
// Claude Code. Complex work is now tracked via the TodoList in ExecutionContext,
// which mirrors Claude Code's TodoWrite tool directly.
//
// If you need structured multi-step plans, build them as a []Todo and pass
// them to ExecutionContext.SetTodos — one todo per step, marked in_progress
// then completed as you go.
