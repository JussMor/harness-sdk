package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// executeFormalPlanWithTasks uses SDK's ExecutionContext + RunSubagentsInParallel
// to execute LLM-proposed tasks in parallel. Replaces PlanProvider + WorkflowEngine.
func executeFormalPlanWithTasks(ctx context.Context, execCtx ab.ExecutionContext, runtime *agentRuntime, messages []ab.ChatMessage, model string, proposedTasks []string) ([]RunnerSummary, string, error) {
	if execCtx == nil || runtime == nil || len(proposedTasks) < 2 {
		return nil, "", nil
	}

	prompt := latestUserPrompt(messages)
	if prompt == "" {
		return nil, "", nil
	}

	// Alignment: propose plan on ExecutionContext
	_ = execCtx.SetPhase(ctx, ab.PhaseAlignment)
	executables := buildExecutablesFromTasks(proposedTasks)
	plan, err := execCtx.Propose(ctx, ab.Plan{
		Title:       "backend-chat formal run",
		Objective:   prompt,
		AutoApprove: true,
		Executables: executables,
	})
	if err != nil {
		return nil, "", fmt.Errorf("propose plan: %w", err)
	}
	_ = execCtx.Approve(ctx, true)
	log.Printf("formal_plan: proposed plan %s with %d executables", plan.ID, len(plan.Executables))

	_ = execCtx.SetPhase(ctx, ab.PhasePreparation)
	_ = execCtx.SetPhase(ctx, ab.PhaseExecution)

	// Spawn all ready executables as parallel subagents
	ready := plan.NextReady()
	subagents := make([]ab.Subagent, 0, len(ready))
	for i, exec := range ready {
		task := strings.TrimSpace(exec.Description)
		if task == "" {
			task = strings.TrimSpace(exec.Name)
		}
		subagents = append(subagents, ab.Subagent{
			ID:      fmt.Sprintf("runner_%d", i+1),
			Task:    task,
			Engine:  runtime.subagentEngine,
			MaxTurns: 4,
			Timeout: 30 * time.Second,
		})
		_ = execCtx.UpdateExecutable(ctx, exec.ID, ab.ExecStatusInProgress, "")
		log.Printf("formal_plan: queued subagent %s task=%q", subagents[len(subagents)-1].ID, previewText(task, 80))
	}

	results := ab.RunSubagentsInParallel(ctx, subagents)

	_ = execCtx.SetPhase(ctx, ab.PhaseVerification)

	runners := make([]RunnerSummary, 0, len(results))
	for i, res := range results {
		execID := fmt.Sprintf("exec_%d", i+1)
		summary := RunnerSummary{
			ID:    res.ID,
			Task:  res.Task,
			Model: model,
		}
		if res.Error != nil {
			summary.Status = "failure"
			summary.Result = res.Error.Error()
			_ = execCtx.UpdateExecutable(ctx, execID, ab.ExecStatusFailed, res.Error.Error())
		} else {
			summary.Status = "success"
			summary.Result = res.Output
			_ = execCtx.UpdateExecutable(ctx, execID, ab.ExecStatusCompleted, res.Output)
		}
		log.Printf("formal_plan: subagent %s status=%s", res.ID, summary.Status)
		runners = append(runners, summary)
	}

	_ = execCtx.SetPhase(ctx, ab.PhaseClosure)

	summary := summarizePlanForPrompt(execCtx.ActivePlan())
	return runners, summary, nil
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
