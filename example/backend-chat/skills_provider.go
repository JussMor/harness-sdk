package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	ab "github.com/everfaz/autobuild-sdk"
)

type fileSkillProvider struct {
	mu     sync.RWMutex
	all    map[string]*ab.Skill
	loaded map[string]*ab.Skill
	names  []string
}

func newFileSkillProvider(skills []*ab.Skill) *fileSkillProvider {
	index := make(map[string]*ab.Skill, len(skills))
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		if skill == nil {
			continue
		}
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		index[name] = skill
		names = append(names, name)
	}
	sort.Strings(names)
	return &fileSkillProvider{all: index, loaded: make(map[string]*ab.Skill), names: names}
}

func loadBackendSkills() (ab.SkillProvider, error) {
	paths := []string{
		"skills",
		filepath.Join("example", "backend-chat", "skills"),
		filepath.Join("..", "backend-chat", "skills"),
	}

	for _, dir := range paths {
		skills, err := ab.LoadSkillsDir(dir)
		if err != nil || len(skills) == 0 {
			continue
		}
		return newFileSkillProvider(skills), nil
	}

	return nil, fmt.Errorf("no SDK skills directory available")
}

func (p *fileSkillProvider) Load(_ context.Context, name string) (*ab.Skill, error) {
	skill, err := p.Get(context.Background(), name)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.loaded[skill.Name] = skill
	p.mu.Unlock()
	return skill, nil
}

func (p *fileSkillProvider) Unload(_ context.Context, name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("skill name is required")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.loaded, trimmed)
	return nil
}

func (p *fileSkillProvider) Loaded(_ context.Context) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.loaded))
	for name := range p.loaded {
		names = append(names, name)
	}
	return names
}

func (p *fileSkillProvider) Match(_ context.Context, text string) ([]ab.SkillMatch, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	matches := make([]ab.SkillMatch, 0)
	for _, name := range p.names {
		skill := p.all[name]
		if skill == nil {
			continue
		}
		score := skill.MatchScore(text)
		if score > 0 {
			matches = append(matches, ab.SkillMatch{
				Skill:     skill,
				Score:     score,
				MatchedOn: text,
			})
		}
	}
	return matches, nil
}

func (p *fileSkillProvider) List(_ context.Context) ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.names))
	copy(out, p.names)
	return out, nil
}

func (p *fileSkillProvider) Get(_ context.Context, name string) (*ab.Skill, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, fmt.Errorf("skill name is required")
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	skill, ok := p.all[trimmed]
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", trimmed)
	}
	return skill, nil
}

// ── ReloadableSkillProvider interface ─────────────────────────────────────────

func (p *fileSkillProvider) AddOrReplace(skill *ab.Skill) {
	if skill == nil || strings.TrimSpace(skill.Name) == "" {
		return
	}
	name := strings.TrimSpace(skill.Name)

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.all[name]; !exists {
		p.names = append(p.names, name)
		sort.Strings(p.names)
	}
	p.all[name] = skill
}

func (p *fileSkillProvider) Remove(skillID string) {
	trimmed := strings.TrimSpace(skillID)
	if trimmed == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.all, trimmed)
	delete(p.loaded, trimmed)
	for i, n := range p.names {
		if n == trimmed {
			p.names = append(p.names[:i], p.names[i+1:]...)
			break
		}
	}
}

var _ ab.ReloadableSkillProvider = (*fileSkillProvider)(nil)
