package main

import (
	"path/filepath"

	ab "github.com/everfaz/autobuild-sdk"
)

func newModeEngine(provider ab.LLMProvider, model string, logContext RuntimeLogContext) (*ab.Engine, *agentRuntime, error) {
	modes, err := loadBackendModes()
	if err != nil {
		return nil, nil, err
	}

	skills, _ := loadBackendSkills()
	memory, _ := loadBackendMemory()
	runtime := newAgentRuntime(provider, model, logContext, skills, memory)

	options := []ab.Option{
		ab.WithLLM(provider),
		ab.WithModes(modes),
		ab.WithToolRegistry(runtime.tools),
		ab.WithThreads(runtime.threads),
		ab.WithEventBus(runtime.events),
	}
	if skills != nil {
		options = append(options, ab.WithSkills(skills))
	}
	if memory != nil {
		options = append(options, ab.WithMemory(memory))
	}

	return ab.New(options...), runtime, nil
}

func loadBackendModes() (ab.ModeProvider, error) {
	return ab.LoadModeProviderFromDirs(
		"modes",
		filepath.Join("example", "backend-chat", "modes"),
		filepath.Join("..", "backend-chat", "modes"),
	)
}
