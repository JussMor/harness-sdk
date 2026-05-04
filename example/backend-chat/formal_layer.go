package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// executeFormalPlanFromProposedPlan runs independent executable steps in parallel
// after Runtime alignment has already produced a structured plan.
func executeFormalPlanFromProposedPlan(ctx context.Context, execCtx ab.ExecutionContext, runtime *agentRuntime, proposed *ab.Plan, model string) ([]RunnerSummary, string, error) {
	if execCtx == nil || runtime == nil || proposed == nil {
		return nil, "", nil
	}
	if len(proposed.Executables) < 2 {
		return nil, "", nil
	}

	// Runtime alignment already proposed the plan, but keep a fallback in case
	// this helper is called with a detached plan.
	plan := execCtx.ActivePlan()
	if plan == nil {
		var err error
		plan, err = execCtx.Propose(ctx, *proposed)
		if err != nil {
			return nil, "", fmt.Errorf("register plan: %w", err)
		}
		_ = execCtx.Approve(ctx, true)
	}

	ready := plan.NextReady()
	if len(ready) < 2 {
		return nil, "", nil
	}

	_ = execCtx.SetPhase(ctx, ab.PhaseExecution)

	subagents := make([]ab.Subagent, 0, len(ready))
	for _, exec := range ready {
		task := strings.TrimSpace(exec.Description)
		if task == "" {
			task = strings.TrimSpace(exec.Name)
		}
		if task == "" {
			continue
		}

		_ = execCtx.UpdateExecutable(ctx, exec.ID, ab.ExecStatusQueued, "")
		_ = execCtx.UpdateExecutable(ctx, exec.ID, ab.ExecStatusInProgress, "")

		subagents = append(subagents, ab.Subagent{
			ID:       exec.ID,
			Task:     task,
			Engine:   runtime.subagentEngine,
			MaxTurns: 4,
			Timeout:  30 * time.Second,
		})
		log.Printf("formal_plan: queued subagent %s task=%q", exec.ID, previewText(task, 80))
	}
	if len(subagents) < 2 {
		return nil, "", nil
	}

	results := ab.RunSubagentsInParallel(ctx, subagents)

	_ = execCtx.SetPhase(ctx, ab.PhaseVerification)

	runners := make([]RunnerSummary, 0, len(results))
	for _, res := range results {
		summary := RunnerSummary{
			ID:    res.ID,
			Task:  res.Task,
			Model: model,
		}
		if res.Error != nil {
			summary.Status = "failure"
			summary.Result = res.Error.Error()
			_ = execCtx.UpdateExecutable(ctx, res.ID, ab.ExecStatusFailed, res.Error.Error())
		} else {
			summary.Status = "success"
			summary.Result = res.Output
			_ = execCtx.UpdateExecutable(ctx, res.ID, ab.ExecStatusCompleted, res.Output)
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
