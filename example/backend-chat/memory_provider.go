package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	ab "github.com/everfaz/autobuild-sdk"
	sdkmemory "github.com/everfaz/autobuild-sdk/providers/memory"
)

// loadBackendMemory initializes the LayeredFilesystemMemory provider from
// the SDK. It creates the standard directory structure expected by
// DefaultMemoryRoots:
//
//	{root}/user/profile/    — user preferences, identity
//	{root}/user/facts/      — inferred and explicit facts about the user
//	{root}/project/         — project context, decisions, workflow state
//
// Returns a LayeredMemoryProvider (superset of MemoryProvider), and separately
// returns the MemoryRoot configuration so the Runtime can inject labeled
// sections into LayerMemory during orientation.
func loadBackendMemory() (ab.MemoryProvider, []ab.MemoryRoot, error) {
	root := resolveMemoryRoot()

	// Create all subdirs that DefaultMemoryRoots will read
	dirs := []string{
		filepath.Join(root, "user", "profile"),
		filepath.Join(root, "user", "facts"),
		filepath.Join(root, "project"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create memory dir %s: %w", dir, err)
		}
	}

	provider, err := sdkmemory.NewLayeredFilesystem(root)
	if err != nil {
		return nil, nil, fmt.Errorf("layered filesystem memory: %w", err)
	}

	log.Printf("backend memory: root=%s (BM25 search, layered)", root)
	return provider, ab.DefaultMemoryRoots, nil
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
