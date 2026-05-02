package main

import (
	"context"
	"fmt"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
)

// BuildSystemPrompt assembles the full system prompt that would be sent to
// the LLM. It follows the Obvious boot sequence:
//
//  1. Base mode prompt (from ModeProvider)
//  2. Active skills (loaded via SkillProvider trigger matching)
//  3. Memory context (user preferences + project state)
//  4. Tool surface (filtered by mode's allow/deny list)
//  5. Runtime context (date, thread, project)
//
// The SDK provides the building blocks; this function shows how to compose them.
func BuildSystemPrompt(ctx context.Context, engine *ab.Engine, modeID string, userRequest string) string {
	var sections []string

	// ─── 1. Mode prompt ────────────────────────────────────────────────
	mode, err := engine.Modes.Get(ctx, modeID)
	if err != nil {
		mode = &ab.Mode{PromptContent: "You are a helpful agent."}
	}
	sections = append(sections, fmt.Sprintf("# System Prompt (mode: %s)\n\n%s", mode.Name, mode.PromptContent))

	// ─── 2. Skills — match triggers, auto-load ─────────────────────────
	if engine.HasSkills() {
		matched, _ := engine.Skills.Match(ctx, userRequest)
		if len(matched) > 0 {
			var skillBlocks []string
			for _, sk := range matched {
				// Load into active context (persists for the thread)
				_, _ = engine.Skills.Load(ctx, sk.Name)
				skillBlocks = append(skillBlocks, fmt.Sprintf("### Skill: %s\n%s", sk.Name, sk.Content))

				// If the skill grants extra tools, note them
				if len(sk.GrantedTools) > 0 {
					skillBlocks = append(skillBlocks,
						fmt.Sprintf("  (grants tools: %s)", strings.Join(sk.GrantedTools, ", ")))
				}
			}
			sections = append(sections, "## Active Skills\n\n"+strings.Join(skillBlocks, "\n\n"))
		}
	}

	// ─── 3. Memory — user prefs + project context ──────────────────────
	if engine.HasMemory() {
		var memBlocks []string

		// User preferences
		prefs, err := engine.Memory.View(ctx, ab.ScopeUser, "/profile/preferences.md")
		if err == nil && prefs != "" {
			memBlocks = append(memBlocks, "### User Preferences\n"+prefs)
		}

		// Project README
		readme, err := engine.Memory.View(ctx, ab.ScopeProject, "/README.md")
		if err == nil && readme != "" {
			memBlocks = append(memBlocks, "### Project Context\n"+readme)
		}

		if len(memBlocks) > 0 {
			sections = append(sections, "## Memory\n\n"+strings.Join(memBlocks, "\n\n"))
		}
	}

	// ─── 4. Available tools (filtered by mode) ─────────────────────────
	if engine.HasTools() {
		var toolLines []string
		for _, tool := range engine.Tools.List() {
			if mode.IsToolAllowed(tool.Name) {
				toolLines = append(toolLines, fmt.Sprintf("- **%s** (%s): %s",
					tool.Name, tool.Category, tool.Description))
			}
		}
		if len(toolLines) > 0 {
			sections = append(sections, "## Available Tools\n\n"+strings.Join(toolLines, "\n"))
		}
	}

	// ─── 5. Runtime context ────────────────────────────────────────────
	sections = append(sections, fmt.Sprintf(`## Context

<context>
Current Date: May 2026
Mode: %s
Tools: %d available
Skills: loaded via trigger match
</context>`, mode.Name, len(engine.Tools.List())))

	return strings.Join(sections, "\n\n---\n\n")
}
