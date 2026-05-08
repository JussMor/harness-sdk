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

	// Strip routing prefix from the parent model so subagents always receive
	// a bare model name (e.g. "claude-haiku-4-5-20251001", not "anthropic/...").
	effectiveModel := runtime.modelName
	if _, modelOnly := ab.ParseModelRef(effectiveModel); modelOnly != "" {
		effectiveModel = modelOnly
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
			Model:    effectiveModel,
			MaxTurns: 6,
			Timeout:  120 * time.Second, // matches runner_runtime default
		})
		log.Printf("formal_plan: queued subagent %s model=%s task=%q", exec.ID, effectiveModel, previewText(task, 80))
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
		log.Printf("formal_plan: subagent %s status=%s turns=%d", res.ID, status, res.Turns)
		runners = append(runners, *res)
	}

	return runners, nil
}
