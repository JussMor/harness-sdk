package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	ab "github.com/everfaz/autobuild-sdk"
	sdkmemory "github.com/everfaz/autobuild-sdk/providers/memory"
)

// loadBackendMemory initializes the FilesystemMemory provider from the SDK.
// Layout follows the Claude Code memdir contract:
//
//	{root}/user/MEMORY.md     — index of user-scoped memory files (auto-seeded)
//	{root}/user/<topic>.md    — typed memory files (frontmatter: type=user|…)
//	{root}/project/MEMORY.md  — index of project-scoped memory files
//	{root}/project/<topic>.md — typed memory files
//
// The runtime injects MEMORY.md into the system prompt and the memory tool
// surfaces the per-turn manifest as a <system-reminder>. Individual files
// are read on demand by the model via memory.view.
func loadBackendMemory() (ab.MemoryProvider, []ab.MemoryRoot, error) {
	root := resolveMemoryRoot()

	dirs := []string{
		filepath.Join(root, "user"),
		filepath.Join(root, "project"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create memory dir %s: %w", dir, err)
		}
		// Seed an empty MEMORY.md index so the LLM has a target for pointers.
		idx := filepath.Join(dir, "MEMORY.md")
		if _, err := os.Stat(idx); os.IsNotExist(err) {
			seed := "# Memory index\n\nPointers to memory files in this scope. " +
				"Keep entries to a single line each.\n\n" +
				"<!-- Format: - [Title](file.md) — one-line hook -->\n"
			_ = os.WriteFile(idx, []byte(seed), 0o644)
		}
	}

	provider, err := sdkmemory.NewFilesystem(root)
	if err != nil {
		return nil, nil, fmt.Errorf("filesystem memory: %w", err)
	}

	var mem ab.MemoryProvider = provider
	log.Printf("backend memory: root=%s (memdir layout)", root)

	return mem, ab.DefaultMemoryRoots, nil
}

// resolveMemoryRoot finds or creates the memory directory.
// Priority: BACKEND_MEMORY_ROOT env var → ./memory → relative fallbacks.
func resolveMemoryRoot() string {
	if env := os.Getenv("BACKEND_MEMORY_ROOT"); env != "" {
		return env
	}
	candidates := []string{
		"memory",
		filepath.Join("example", "backend-chat", "memory"),
		filepath.Join("..", "backend-chat", "memory"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Dir(c)); err == nil {
			return c
		}
	}
	return "memory"
}
