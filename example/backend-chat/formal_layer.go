package main

import (
	"context"
	"log"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// executeFormalPlanFromProposedPlan runs independent executable steps in parallel
// from the Runtime-proposed plan and returns SDK-native subagent results.
func executeFormalPlanFromProposedPlan(ctx context.Context, runtime *agentRuntime, proposed *ab.Plan) ([]ab.SubagentResult, error) {
	if runtime == nil || proposed == nil {
		return nil, nil
	}
	if len(proposed.Executables) < 2 {
		return nil, nil
	}

	ready := proposed.NextReady()
	if len(ready) < 2 {
		return nil, nil
	}

	subagents := make([]ab.Subagent, 0, len(ready))
	for _, exec := range ready {
		task := strings.TrimSpace(exec.Description)
		if task == "" {
			task = strings.TrimSpace(exec.Name)
		}
		if task == "" {
			continue
		}

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
		return nil, nil
	}

	results := ab.RunSubagentsInParallel(ctx, subagents)
	runners := make([]ab.SubagentResult, 0, len(results))
	for _, res := range results {
		status := "success"
		if res.Error != nil {
			status = "failure"
		}
		log.Printf("formal_plan: subagent %s status=%s", res.ID, status)
		runners = append(runners, *res)
	}

	return runners, nil
}

func latestUserPrompt(messages []ab.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == ab.RoleUser {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}
