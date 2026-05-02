// Package main demonstrates a complete autobuild-sdk implementation:
// skills and modes loaded from markdown files with YAML frontmatter,
// system prompt assembly, DAG execution loop, and event-driven
// orchestration — all runnable with `go run ./example`.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
)

func main() {
	ctx := context.Background()

	// ─── 0. Resolve paths relative to the executable ───────────────────
	exampleDir := findExampleDir()
	skillsDir := filepath.Join(exampleDir, "skills")
	modesDir := filepath.Join(exampleDir, "modes")

	// ─── 1. Load skills from SKILL.md files ────────────────────────────
	fmt.Println("═══ LOADING SKILLS ═══")
	parsedSkills, err := ab.LoadSkillsDir(skillsDir)
	if err != nil {
		log.Fatalf("load skills: %v", err)
	}
	skills := NewSkillStore()
	for _, sk := range parsedSkills {
		skills.Add(sk)
		fmt.Printf("  ✓ %s v%s [%s] (%d triggers)\n",
			sk.Meta.Name, sk.Meta.Version, sk.Meta.Category, len(sk.Triggers))
	}

	// ─── 2. Load modes from system.md files ────────────────────────────
	fmt.Println("\n═══ LOADING MODES ═══")
	parsedModes, err := ab.LoadModesDir(modesDir)
	if err != nil {
		log.Fatalf("load modes: %v", err)
	}
	modes := NewModeStoreFromParsed(parsedModes)
	for _, m := range parsedModes {
		strategy := "default"
		if m.PromptStrategy != "" {
			strategy = string(m.PromptStrategy)
		}
		fmt.Printf("  ✓ %s (base: %s, strategy: %s)\n", m.Name, m.BaseModeID, strategy)
	}

	// ─── 3. Build LLM providers ───────────────────────────────────────
	//
	// SimulatedLLM behaves like a real agent: reads memory, creates
	// checkpoints, runs commands, creates documents — following the
	// 6-phase workflow. Replace with real Anthropic/OpenAI/Ollama clients.
	anthropicLLM := NewSimulatedLLM("claude-sonnet-4-20250514")
	openaiLLM := NewSimulatedLLM("gpt-4o")

	router := NewStubRouter(anthropicLLM)
	router.Register("claude", anthropicLLM)
	router.Register("gpt", openaiLLM)

	// ─── 4. Build other providers ──────────────────────────────────────
	mem := NewMemoryStore()
	sandbox := &LocalSandbox{}
	threads := NewThreadStore()
	checkpoints := &CheckpointStore{}
	plans := &PlanStore{}
	tasks := &TaskStore{}
	bus := ab.NewEventBus()

	// Seed memory
	_ = mem.Create(ctx, ab.ScopeProject, "/README.md",
		"# My Project\nStatus: planning\nStack: Go + React\n")
	_ = mem.Create(ctx, ab.ScopeUser, "/profile/preferences.md",
		"- Language: Spanish\n- Tone: direct\n")

	// ─── 5. Wire the engine ────────────────────────────────────────────
	engine := ab.New(
		ab.WithMemory(mem),
		ab.WithSandbox(sandbox),
		ab.WithSkills(skills),
		ab.WithThreads(threads),
		ab.WithCheckpoints(checkpoints),
		ab.WithModes(modes),
		ab.WithPlanning(plans),
		ab.WithTasks(tasks),
		ab.WithToolRegistry(buildToolRegistry()),
		ab.WithEventBus(bus),
		ab.WithLLM(anthropicLLM),  // single default LLM
		ab.WithRouter(router),      // or use multi-model routing
	)

	// ─── 6. Subscribe to events ────────────────────────────────────────
	bus.Subscribe(ab.EventRunnerCompleted, func(e ab.Event) {
		fmt.Printf("  [EVENT] Runner completed: %s → %v\n", e.Source, e.Payload["result"])
	})
	bus.Subscribe(ab.EventPhaseAdvanced, func(e ab.Event) {
		fmt.Printf("  [EVENT] Phase: %s → %s\n", e.Payload["from"], e.Payload["to"])
	})
	bus.Subscribe(ab.EventExecutableUpdated, func(e ab.Event) {
		fmt.Printf("  [EVENT] Executable %s → %s\n", e.Payload["exec_id"], e.Payload["status"])
	})
	// Track every agent turn
	bus.Subscribe("agent.turn", func(e ab.Event) {
		fmt.Printf("  [TURN %v] tools=%v tokens=%v\n",
			e.Payload["turn"], e.Payload["tool_calls"], e.Payload["tokens"])
	})

	// ─── 7. Build system prompt ────────────────────────────────────────
	fmt.Println("\n═══ SYSTEM PROMPT ═══")
	prompt := BuildSystemPrompt(ctx, engine, "balanced", "Build a new auth feature")
	// Print just first 5 lines to keep output clean
	lines := strings.SplitN(prompt, "\n", 6)
	for _, l := range lines[:min(5, len(lines))] {
		fmt.Println(l)
	}
	fmt.Println("  ...")

	// ─── 8. AUTONOMOUS AGENT LOOP ─────────────────────────────────────
	// RunAgentLoop is fully agnostic — no Engine dependency.
	// You inject exactly what you need: Provider, Tools, Events.
	//
	// Option A (agnostic): pass everything explicitly
	// Option B (convenience): RunAgentLoopWithEngine extracts from Engine
	//
	// Below we use Option A to show the agnostic API:
	fmt.Println("\n═══ AUTONOMOUS AGENT (RunAgentLoop) ═══")
	fmt.Println("  User: Build a new auth feature for the API")
	fmt.Println()

	result, err := ab.RunAgentLoop(ctx, ab.AgentLoopConfig{
		// ── Only Provider is required ──
		Provider: anthropicLLM,

		// ── Everything else is optional ──
		Model:        "claude-sonnet-4-20250514",
		SystemPrompt: prompt,
		Tools:        engine.Tools,
		Sandbox:      engine.Sandbox,
		SandboxID:    "cmp_001",
		Events:       bus,
		MaxTurns:     10,

		// ── Hooks for observability ──
		OnTurn: func(turn int, resp *ab.ChatResponse) bool {
			if len(resp.ToolCalls) > 0 {
				names := make([]string, len(resp.ToolCalls))
				for i, tc := range resp.ToolCalls {
					names[i] = tc.Name
				}
				fmt.Printf("  Turn %d → LLM calls: %s\n", turn, strings.Join(names, ", "))
			}
			return true // continue
		},
	}, []ab.ChatMessage{
		{Role: ab.RoleUser, Content: "Build a new auth feature for the API with JWT tokens"},
	})
	if err != nil {
		log.Fatalf("agent loop error: %v", err)
	}

	fmt.Printf("\n  Agent finished: %s (%d turns, %d tokens)\n",
		result.StopReason, result.TotalTurns, result.TotalUsage.TotalTokens)
	fmt.Printf("  Response: %s\n", result.FinalContent[:min(120, len(result.FinalContent))])
	fmt.Println()

	// ─── 9. Create a plan with DAG ────────────────────────────────────
	fmt.Println("═══ PLAN & DAG EXECUTION ═══")
	plan := ab.Plan{
		Title:     "Ship Auth System",
		Objective: "Add JWT authentication to the API",
		Executables: []ab.Executable{
			{ID: "exe_schema", Name: "DB migration — add users table", Status: ab.ExecStatusPlanned},
			{ID: "exe_middleware", Name: "Auth middleware", Dependencies: []string{"exe_schema"}, Status: ab.ExecStatusPlanned},
			{ID: "exe_tests", Name: "Integration tests", Dependencies: []string{"exe_middleware"}, Status: ab.ExecStatusPlanned},
			{ID: "exe_docs", Name: "API documentation", Dependencies: []string{"exe_middleware"}, Status: ab.ExecStatusPlanned},
		},
	}

	RunExecutionLoop(ctx, engine, &plan, bus)

	// ─── 10. Task with gate and condition ─────────────────────────────
	fmt.Println("\n═══ TASK WORKFLOW ═══")
	DemoTaskWorkflow(ctx, engine)

	// ─── 11. Skill trigger matching ───────────────────────────────────
	fmt.Println("\n═══ SKILL MATCHING ═══")
	DemoSkillMatching(ctx, engine, "Create a document report for Q4 sales")
	DemoSkillMatching(ctx, engine, "Fix the login button CSS")

	fmt.Println("\n✓ Example complete")
}

// RunExecutionLoop simulates the core autobuild execution loop (Section 17
// of system.md): query unblocked → spawn → wait → advance.
func RunExecutionLoop(ctx context.Context, engine *ab.Engine, plan *ab.Plan, bus ab.EventBus) {
	wave := 0

	for {
		ready := plan.NextReady()
		if len(ready) == 0 {
			if plan.IsComplete() {
				fmt.Println("  ✓ All executables finished")
			} else {
				fmt.Println("  ⏳ Waiting — no unblocked executables")
			}
			break
		}

		wave++
		fmt.Printf("\n  Wave %d — %d executable(s) ready:\n", wave, len(ready))

		for _, exe := range ready {
			// planned → queued
			transition(plan, exe.ID, ab.ExecStatusQueued, bus)

			// Spawn thread
			mode := selectMode(exe)
			threadID, err := engine.Threads.Spawn(ctx, ab.Runner{
				Tier: ab.RunnerTierMini,
				Task: fmt.Sprintf("Implement: %s", exe.Name),
				ResourceBundle: []ab.ResourceRef{
					{ID: exe.ID, Type: "executable", Description: exe.Name},
				},
			})
			if err != nil {
				log.Printf("  ✗ Failed to spawn %s: %v", exe.ID, err)
				transition(plan, exe.ID, ab.ExecStatusFailed, bus)
				continue
			}

			// queued → in_progress
			transition(plan, exe.ID, ab.ExecStatusInProgress, bus)
			fmt.Printf("    Spawned thread %s (mode=%s) for %s\n", threadID, mode, exe.ID)
		}

		// Simulate: all threads in this wave complete successfully
		for _, exe := range ready {
			e := plan.ExecutableByID(exe.ID)
			if e != nil && e.Status == ab.ExecStatusInProgress {
				// in_progress → completed (non-PR work skips in_review)
				transition(plan, exe.ID, ab.ExecStatusCompleted, bus)

				bus.Publish(ab.Event{
					Type:    ab.EventRunnerCompleted,
					Source:  exe.ID,
					Payload: map[string]any{"result": "success", "thread_id": "th_sim"},
				})
			}
		}
	}
}

// DemoTaskWorkflow shows a Task with steps, conditions, and a gate.
func DemoTaskWorkflow(ctx context.Context, engine *ab.Engine) {
	urgent := "urgent"
	task := ab.Task{
		Name:        "Process Support Ticket",
		Description: "Classify and resolve tickets",
		Steps: []ab.Step{
			{
				ID: "classify", Content: "Classify ticket priority", Position: 0,
				Condition: &ab.Condition{
					Field: "priority", Operator: ab.OpEquals, Value: "urgent",
					IfTrue: "escalate", IfFalse: "auto_resolve",
				},
			},
			{
				ID: "escalate", Content: "Escalate urgent ticket", Position: 1,
				Gate: &ab.Gate{
					Type: ab.GateTypeApproval, Approvers: []string{"lead@example.com"},
					OnReject: ab.OnRejectRouteToStep, RejectTargetStepID: "auto_resolve",
				},
				NextStepID: &urgent, // → manual_resolve would be here
			},
			{
				ID: "auto_resolve", Content: "Auto-resolve ticket", Position: 2,
			},
		},
	}

	fmt.Printf("  Task: %s (%d steps)\n", task.Name, len(task.Steps))
	fmt.Printf("  First step: %s\n", task.FirstStep().ID)

	// Simulate: classify → condition evaluates → route
	step := task.StepByID("classify")
	simulatedOutput := "urgent"
	if step.Condition != nil {
		if simulatedOutput == step.Condition.Value {
			fmt.Printf("  Condition: %s == %q → route to %q\n",
				step.Condition.Field, step.Condition.Value, step.Condition.IfTrue)
		} else {
			fmt.Printf("  Condition: %s != %q → route to %q\n",
				step.Condition.Field, step.Condition.Value, step.Condition.IfFalse)
		}
	}

	// Show gate on escalate step
	escalate := task.StepByID("escalate")
	fmt.Printf("  Gate on %q: type=%s, approvers=%v, onReject=%s→%s\n",
		escalate.ID, escalate.Gate.Type, escalate.Gate.Approvers,
		escalate.Gate.OnReject, escalate.Gate.RejectTargetStepID)
}

// DemoSkillMatching shows how skills are matched by triggers.
func DemoSkillMatching(ctx context.Context, engine *ab.Engine, userRequest string) {
	matches, _ := engine.Skills.Match(ctx, userRequest)
	if len(matches) == 0 {
		fmt.Printf("  %q → no skills matched\n", userRequest)
		return
	}
	names := make([]string, len(matches))
	for i, s := range matches {
		names[i] = s.Name
	}
	fmt.Printf("  %q → matched: [%s]\n", userRequest, strings.Join(names, ", "))
}

// ─── Helpers ───────────────────────────────────────────────────────────

func transition(plan *ab.Plan, execID string, to ab.ExecutableStatus, bus ab.EventBus) {
	e := plan.ExecutableByID(execID)
	if e == nil {
		return
	}
	if err := ab.ValidateTransition(e.Status, to); err != nil {
		fmt.Printf("    ✗ %s: %v\n", execID, err)
		return
	}
	from := e.Status
	e.Status = to
	fmt.Printf("    %s: %s → %s\n", execID, from, to)

	bus.Publish(ab.Event{
		Type:    ab.EventExecutableUpdated,
		Source:  execID,
		Payload: map[string]any{"exec_id": execID, "status": string(to), "from": string(from)},
	})
}

func selectMode(exe ab.Executable) string {
	// Section 20 of system.md: PR work → code-agent, docs → auto-plus
	if strings.Contains(strings.ToLower(exe.Name), "doc") {
		return "auto-plus"
	}
	return "code-agent"
}

// findExampleDir resolves the path to the example/ directory.
// It checks for a "skills" subdirectory relative to the working directory,
// then falls back to the directory of os.Args[0].
func findExampleDir() string {
	// Try current working directory first (go run sets this)
	candidates := []string{"."}

	// Try the directory containing the binary
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}

	// Try relative to working directory
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "example"))
	}

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "skills")); err == nil {
			return dir
		}
	}

	log.Fatal("cannot find example directory — run from the sdk/ root with: go run ./example")
	return ""
}
