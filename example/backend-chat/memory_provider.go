package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	ab "github.com/everfaz/autobuild-sdk"
	sdkembedders "github.com/everfaz/autobuild-sdk/providers/embedders"
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

	// Wrap with Hybrid search (BM25 + vector via RRF)
	var mem ab.MemoryProvider = provider
	if apiKey := os.Getenv("VOYAGE_API_KEY"); apiKey != "" {
		embedder := sdkembedders.NewVoyage(apiKey, "voyage-3")
		mem = ab.NewHybridMemorySearch(provider, embedder)
		log.Printf("backend memory: root=%s (hybrid BM25+Voyage search)", root)
	} else {
		embedder := sdkembedders.NewLocal(384)
		mem = ab.NewHybridMemorySearch(provider, embedder)
		log.Printf("backend memory: root=%s (hybrid BM25+local-embedder search)", root)
	}

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
