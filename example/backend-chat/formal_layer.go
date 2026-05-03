package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

type inMemoryPlanProvider struct {
	mu      sync.RWMutex
	events  ab.EventBus
	nextID  atomic.Uint64
	plans   map[string]*ab.Plan
}

func newInMemoryPlanProvider(events ab.EventBus) *inMemoryPlanProvider {
	return &inMemoryPlanProvider{events: events, plans: make(map[string]*ab.Plan)}
}

func (p *inMemoryPlanProvider) Propose(_ context.Context, plan ab.Plan) (*ab.Plan, error) {
	if len(plan.Executables) == 0 {
		return nil, fmt.Errorf("plan requires at least one executable")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if strings.TrimSpace(plan.ID) == "" {
		plan.ID = fmt.Sprintf("plan_%d", p.nextID.Add(1))
	}
	for i := range plan.Executables {
		if strings.TrimSpace(plan.Executables[i].ID) == "" {
			plan.Executables[i].ID = fmt.Sprintf("exec_%d", i+1)
		}
		if strings.TrimSpace(string(plan.Executables[i].Status)) == "" {
			plan.Executables[i].Status = ab.ExecStatusPlanned
		}
	}
	copied := plan
	copied.Executables = append([]ab.Executable(nil), plan.Executables...)
	p.plans[copied.ID] = &copied
	out := copied
	return &out, nil
}

func (p *inMemoryPlanProvider) Approve(_ context.Context, planID string, autoApprove bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	plan, ok := p.plans[planID]
	if !ok {
		return fmt.Errorf("plan not found: %s", planID)
	}
	plan.Approved = true
	plan.AutoApprove = autoApprove
	if p.events != nil {
		p.events.Publish(ab.Event{
			Type:   ab.EventPlanApproved,
			Source: planID,
			Payload: map[string]any{
				"plan_id":      planID,
				"auto_approve": autoApprove,
			},
		})
	}
	return nil
}

func (p *inMemoryPlanProvider) UpdateStatus(_ context.Context, planID string, execID string, status ab.ExecutableStatus, result string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	plan, ok := p.plans[planID]
	if !ok {
		return fmt.Errorf("plan not found: %s", planID)
	}
	exec := plan.ExecutableByID(execID)
	if exec == nil {
		return fmt.Errorf("executable not found: %s", execID)
	}
	if err := ab.ValidateTransition(exec.Status, status); err != nil {
		return err
	}
	exec.Status = status
	exec.Result = result
	if p.events != nil {
		p.events.Publish(ab.Event{
			Type:   ab.EventExecutableUpdated,
			Source: execID,
			Payload: map[string]any{
				"plan_id": planID,
				"exec_id": execID,
				"status":  string(status),
				"result":  result,
			},
		})
	}
	return nil
}

func (p *inMemoryPlanProvider) GetPlan(_ context.Context, planID string) (*ab.Plan, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	plan, ok := p.plans[planID]
	if !ok {
		return nil, fmt.Errorf("plan not found: %s", planID)
	}
	copied := *plan
	copied.Executables = append([]ab.Executable(nil), plan.Executables...)
	return &copied, nil
}

type inMemoryWorkflowEngine struct {
	mu      sync.RWMutex
	events  ab.EventBus
	phase   ab.Phase
	hooks   map[ab.Phase][]ab.PhaseHook
	todos   []ab.Todo
}

func newInMemoryWorkflowEngine(events ab.EventBus) *inMemoryWorkflowEngine {
	return &inMemoryWorkflowEngine{
		events: events,
		phase:  ab.PhaseOrientation,
		hooks:  make(map[ab.Phase][]ab.PhaseHook),
	}
}

func (w *inMemoryWorkflowEngine) CurrentPhase() ab.Phase {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.phase
}

func (w *inMemoryWorkflowEngine) Advance(ctx context.Context) error {
	w.mu.RLock()
	from := w.phase
	w.mu.RUnlock()
	if from >= ab.PhaseClosure {
		return nil
	}
	return w.SetPhase(ctx, from+1)
}

func (w *inMemoryWorkflowEngine) SetPhase(ctx context.Context, to ab.Phase) error {
	w.mu.RLock()
	from := w.phase
	hooks := append([]ab.PhaseHook(nil), w.hooks[to]...)
	w.mu.RUnlock()

	for _, hook := range hooks {
		if err := hook(ctx, from, to); err != nil {
			return err
		}
	}

	w.mu.Lock()
	w.phase = to
	w.mu.Unlock()

	if w.events != nil {
		w.events.Publish(ab.Event{
			Type:   ab.EventPhaseAdvanced,
			Source: "workflow",
			Payload: map[string]any{
				"from": from.String(),
				"to":   to.String(),
			},
		})
	}
	return nil
}

func (w *inMemoryWorkflowEngine) RegisterHook(target ab.Phase, hook ab.PhaseHook) {
	if hook == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hooks[target] = append(w.hooks[target], hook)
}

func (w *inMemoryWorkflowEngine) Todos() []ab.Todo {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]ab.Todo, len(w.todos))
	copy(out, w.todos)
	return out
}

func (w *inMemoryWorkflowEngine) SetTodos(todos []ab.Todo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.todos = append([]ab.Todo(nil), todos...)
}

func executeFormalPlanWithTasks(ctx context.Context, engine *ab.Engine, runtime *agentRuntime, messages []ab.ChatMessage, model string, proposedTasks []string) ([]RunnerSummary, string, error) {
	if engine == nil || engine.Plans == nil || engine.Workflow == nil || runtime == nil || len(proposedTasks) < 2 {
		return nil, "", nil
	}

	prompt := latestUserPrompt(messages)
	if prompt == "" {
		return nil, "", nil
	}

	// State shared across workflow hooks
	var plan *ab.Plan
	var runners []RunnerSummary
	var planErr error
	
	workflow := engine.Workflow

	// Hook: Create Plan in Preparation phase
	workflow.RegisterHook(ab.PhasePreparation, func(ctx context.Context, from, to ab.Phase) error {
		log.Printf("Workflow PhasePreparation: creating plan with %d tasks", len(proposedTasks))
		
		executables := buildExecutablesFromTasks(proposedTasks)
		var err error
		plan, err = engine.Plans.Propose(ctx, ab.Plan{
			Title:       "backend-chat formal run",
			Objective:   prompt,
			AutoApprove: true,
			Executables: executables,
		})
		if err != nil {
			planErr = err
			return err
		}

		if err := engine.Plans.Approve(ctx, plan.ID, true); err != nil {
			planErr = err
			return err
		}
		
		log.Printf("Workflow PhasePreparation: plan created %s with %d executables", plan.ID, len(plan.Executables))
		return nil
	})

	// Hook: Execute Plan in Execution phase
	workflow.RegisterHook(ab.PhaseExecution, func(ctx context.Context, from, to ab.Phase) error {
		if plan == nil {
			return fmt.Errorf("plan not created in preparation phase")
		}
		
		log.Printf("Workflow PhaseExecution: spawning runners for plan %s", plan.ID)
		
		ready := plan.NextReady()
		for _, exec := range ready {
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, exec.ID, ab.ExecStatusQueued, "queued for execution")
			
			task := strings.TrimSpace(exec.Description)
			if task == "" {
				task = strings.TrimSpace(exec.Name)
			}
			
			threadID, spawnErr := runtime.threads.Spawn(ctx, ab.Runner{
				Tier: ab.RunnerTierMini,
				Task: task,
			})
			if spawnErr != nil {
				_ = engine.Plans.UpdateStatus(ctx, plan.ID, exec.ID, ab.ExecStatusFailed, spawnErr.Error())
				continue
			}
			
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, exec.ID, ab.ExecStatusInProgress, "execution started")
			log.Printf("Workflow PhaseExecution: spawned runner %s for exec %s", threadID, exec.ID)
		}
		
		return nil
	})

	// Hook: Collect results in Verification phase
	workflow.RegisterHook(ab.PhaseVerification, func(ctx context.Context, from, to ab.Phase) error {
		if plan == nil {
			return fmt.Errorf("plan not available for verification")
		}
		
		log.Printf("Workflow PhaseVerification: collecting results for plan %s", plan.ID)
		
		// Wait long enough so run.completed is emitted after runner.completed events.
		runners = runtime.threads.Wait(30 * time.Second)
		log.Printf("Workflow PhaseVerification: collected %d runners", len(runners))
		
		return nil
	})

	// Workflow orchestrates the phases
	log.Printf("Workflow: starting formal plan execution with LLM-proposed tasks")
	_ = workflow.SetPhase(ctx, ab.PhaseOrientation)
	_ = workflow.SetPhase(ctx, ab.PhaseAlignment)
	_ = workflow.SetPhase(ctx, ab.PhasePreparation)    // Hook: Plan created
	_ = workflow.SetPhase(ctx, ab.PhaseExecution)      // Hook: Runners spawned
	_ = workflow.SetPhase(ctx, ab.PhaseVerification)   // Hook: Results collected
	_ = workflow.SetPhase(ctx, ab.PhaseClosure)

	if planErr != nil {
		return nil, "", planErr
	}

	var summary string
	if plan != nil {
		summary = summarizePlanForPrompt(plan)
	}
	
	log.Printf("Workflow: formal plan execution complete, plan=%v runners=%d", plan != nil, len(runners))
	return runners, summary, nil
}

func executeFormalPlan(ctx context.Context, engine *ab.Engine, runtime *agentRuntime, messages []ab.ChatMessage, model string) ([]RunnerSummary, string, error) {
	if engine == nil || engine.Plans == nil || runtime == nil {
		return nil, "", nil
	}

	prompt := latestUserPrompt(messages)
	if prompt == "" {
		return nil, "", nil
	}

	chunks := splitPromptIntoTasks(prompt)
	if len(chunks) < 2 {
		chunks = []string{prompt}
	}

	// Plan execution is controlled by useFormalPlan flag from frontend;
	// no heuristic validation needed here

	if engine.Workflow != nil {
		_ = engine.Workflow.SetPhase(ctx, ab.PhaseOrientation)
		_ = engine.Workflow.SetPhase(ctx, ab.PhaseAlignment)
	}

	executables := buildExecutablesFromTasks(chunks)
	plan, err := engine.Plans.Propose(ctx, ab.Plan{
		Title:       "backend-chat formal run",
		Objective:   prompt,
		AutoApprove: true,
		Executables: executables,
	})
	if err != nil {
		return nil, "", err
	}
	if err := engine.Plans.Approve(ctx, plan.ID, true); err != nil {
		return nil, "", err
	}

	if engine.Workflow != nil {
		_ = engine.Workflow.SetPhase(ctx, ab.PhasePreparation)
		_ = engine.Workflow.SetPhase(ctx, ab.PhaseExecution)
	}

	threadByExec := make(map[string]string)
	ready := plan.NextReady()
	for _, exec := range ready {
		_ = engine.Plans.UpdateStatus(ctx, plan.ID, exec.ID, ab.ExecStatusQueued, "queued for execution")
		task := strings.TrimSpace(exec.Description)
		if task == "" {
			task = strings.TrimSpace(exec.Name)
		}
		threadID, spawnErr := runtime.threads.Spawn(ctx, ab.Runner{Tier: ab.RunnerTierMini, Task: task})
		if spawnErr != nil {
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, exec.ID, ab.ExecStatusFailed, spawnErr.Error())
			continue
		}
		threadByExec[exec.ID] = threadID
		_ = engine.Plans.UpdateStatus(ctx, plan.ID, exec.ID, ab.ExecStatusInProgress, "thread: "+threadID)
	}

	runtime.threads.Wait(5 * time.Second)
	snap := runtime.threads.Snapshot()
	byThread := make(map[string]RunnerSummary, len(snap))
	for _, runner := range snap {
		byThread[runner.ID] = runner
	}

	for execID, threadID := range threadByExec {
		runner, ok := byThread[threadID]
		if !ok {
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, execID, ab.ExecStatusFailed, "runner timeout or missing summary")
			continue
		}
		switch runner.Status {
		case string(ab.ObjectiveStatusSuccess):
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, execID, ab.ExecStatusCompleted, runner.Result)
		case string(ab.ObjectiveStatusFailure):
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, execID, ab.ExecStatusFailed, runner.Result)
		default:
			_ = engine.Plans.UpdateStatus(ctx, plan.ID, execID, ab.ExecStatusFailed, "runner did not finish in time")
		}
	}

	if engine.Workflow != nil {
		_ = engine.Workflow.SetPhase(ctx, ab.PhaseVerification)
		_ = engine.Workflow.SetPhase(ctx, ab.PhaseClosure)
	}

	finalPlan, err := engine.Plans.GetPlan(ctx, plan.ID)
	if err != nil {
		return snap, "", nil
	}
	return snap, summarizePlanForPrompt(finalPlan), nil
}

func latestUserPrompt(messages []ab.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == ab.RoleUser {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func buildExecutablesFromTasks(chunks []string) []ab.Executable {
	execs := make([]ab.Executable, 0, len(chunks))
	for i, task := range chunks {
		execs = append(execs, ab.Executable{
			ID:          fmt.Sprintf("exec_%d", i+1),
			Name:        fmt.Sprintf("Subtask %d", i+1),
			Description: task,
			Status:      ab.ExecStatusPlanned,
		})
	}
	return execs
}

func splitPromptIntoTasks(prompt string) []string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return nil
	}

	lines := strings.Split(trimmed, "\n")
	bullets := make([]string, 0)
	for _, line := range lines {
		candidate := strings.TrimSpace(line)
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "-") || strings.HasPrefix(candidate, "*") {
			candidate = strings.TrimSpace(strings.TrimLeft(candidate, "-*"))
			if candidate != "" {
				bullets = append(bullets, candidate)
			}
			continue
		}
		if len(candidate) > 2 && candidate[1] == '.' {
			candidate = strings.TrimSpace(candidate[2:])
			if candidate != "" {
				bullets = append(bullets, candidate)
			}
		}
	}
	if len(bullets) > 0 {
		return bullets
	}

	for _, sep := range []string{";", " then ", " despues ", " después ", " y luego ", " y también "} {
		parts := strings.Split(trimmed, sep)
		if len(parts) >= 2 {
			out := make([]string, 0, len(parts))
			for _, part := range parts {
				candidate := strings.TrimSpace(part)
				if candidate != "" {
					out = append(out, candidate)
				}
			}
			if len(out) > 1 {
				return out
			}
		}
	}

	return []string{trimmed}
}


func summarizePlanForPrompt(plan *ab.Plan) string {
	if plan == nil {
		return ""
	}
	lines := make([]string, 0, len(plan.Executables)+1)
	lines = append(lines, "Plan execution summary:")
	for _, exec := range plan.Executables {
		result := strings.TrimSpace(exec.Result)
		if result == "" {
			result = "no output"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s): %s", exec.Name, exec.Status, previewText(result, 180)))
	}
	return strings.Join(lines, "\n")
}
