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

// loadBackendMemory initializes the FilesystemMemory provider.
//
// Memory follows the Claude Code model: each scope has a single MEMORY.md
// index plus topical files written by the LLM via memory tools. No directory
// scaffolding is created — NewFilesystem ensures user/ and project/ exist;
// everything else is the LLM's responsibility.
func loadBackendMemory() (ab.MemoryProvider, error) {
	root := resolveMemoryRoot()

	provider, err := sdkmemory.NewFilesystem(root)
	if err != nil {
		return nil, fmt.Errorf("filesystem memory: %w", err)
	}

	var mem ab.MemoryProvider = provider
	if apiKey := os.Getenv("VOYAGE_API_KEY"); apiKey != "" {
		embedder := sdkembedders.NewVoyage(apiKey, "voyage-3")
		mem = ab.NewHybridMemorySearch(provider, embedder)
		log.Printf("backend memory: root=%s (hybrid BM25+Voyage search)", root)
	} else {
		log.Printf("backend memory: root=%s (BM25 search)", root)
	}

	return mem, nil
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
