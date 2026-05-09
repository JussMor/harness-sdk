// Package skills provides SkillProvider implementations for the autobuild SDK.
package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// ── InMemorySkillProvider ─────────────────────────────────────────────────────

// InMemorySkillProvider holds skills in memory and tracks which are currently
// loaded. Thread-safe. Use for testing or when skills are static at startup.
//
// Usage:
//
//	skills, _ := autobuild.LoadSkillsDir("./skills")
//	provider := skills_provider.NewInMemory(skills...)
//	engine.Skills = provider
type InMemorySkillProvider struct {
	mu      sync.RWMutex
	skills  map[string]*autobuild.Skill // all available skills
	loaded  map[string]bool             // currently loaded skill names
}

// NewInMemory creates an InMemorySkillProvider with the given skills pre-loaded.
func NewInMemory(skills ...*autobuild.Skill) *InMemorySkillProvider {
	p := &InMemorySkillProvider{
		skills: make(map[string]*autobuild.Skill, len(skills)),
		loaded: make(map[string]bool),
	}
	for _, s := range skills {
		if s != nil {
			p.skills[s.Meta.Name] = s
		}
	}
	return p
}

func (p *InMemorySkillProvider) Load(_ context.Context, name string) (*autobuild.Skill, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	p.loaded[name] = true
	return s, nil
}

func (p *InMemorySkillProvider) Unload(_ context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.loaded, name)
	return nil
}

func (p *InMemorySkillProvider) Loaded(_ context.Context) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.loaded))
	for name := range p.loaded {
		names = append(names, name)
	}
	return names
}

func (p *InMemorySkillProvider) Match(_ context.Context, text string) ([]autobuild.SkillMatch, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var matches []autobuild.SkillMatch
	for _, s := range p.skills {
		score := s.MatchScore(text)
		if score > 0 {
			matches = append(matches, autobuild.SkillMatch{
				Skill:     s,
				Score:     score,
				MatchedOn: "trigger",
			})
		}
	}
	// Sort by score descending
	for i := 1; i < len(matches); i++ {
		for j := i; j > 0 && matches[j].Score > matches[j-1].Score; j-- {
			matches[j], matches[j-1] = matches[j-1], matches[j]
		}
	}
	return matches, nil
}

func (p *InMemorySkillProvider) List(_ context.Context) ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.skills))
	for name := range p.skills {
		names = append(names, name)
	}
	return names, nil
}

func (p *InMemorySkillProvider) Get(_ context.Context, name string) (*autobuild.Skill, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	return s, nil
}

// AddOrReplace adds or replaces a skill — implements ReloadableSkillProvider.
func (p *InMemorySkillProvider) AddOrReplace(skill *autobuild.Skill) {
	if skill == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.skills[skill.Meta.Name] = skill
}

// Remove removes a skill by name — implements ReloadableSkillProvider.
func (p *InMemorySkillProvider) Remove(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.skills, name)
	delete(p.loaded, name)
}

// Verify interfaces at compile time
var _ autobuild.SkillProvider = (*InMemorySkillProvider)(nil)

// ── FilesystemSkillProvider ───────────────────────────────────────────────────

// FilesystemSkillProvider reads skills from a directory of SKILL.md files.
// Skills are loaded lazily on first access and cached in memory.
// Hot reload is supported via SkillReloader (see sdk/skill_reload.go).
//
// Directory layout:
//
//	skills/
//	  writing/
//	    SKILL.md
//	  code-review/
//	    SKILL.md
//
// Usage:
//
//	provider, _ := skills_provider.NewFilesystem("./skills")
//	engine.Skills = provider
type FilesystemSkillProvider struct {
	root string
	mem  *InMemorySkillProvider
	once sync.Once
}

// NewFilesystem creates a FilesystemSkillProvider rooted at dir.
// Skills are parsed from SKILL.md files one level deep.
func NewFilesystem(dir string) (*FilesystemSkillProvider, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("skills dir %q: %w", dir, err)
	}
	return &FilesystemSkillProvider{
		root: dir,
		mem:  NewInMemory(),
	}, nil
}

func (p *FilesystemSkillProvider) load() {
	p.once.Do(func() {
		entries, err := os.ReadDir(p.root)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			mdPath := filepath.Join(p.root, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(mdPath)
			if err != nil {
				continue
			}
			skill, err := autobuild.ParseSkillMarkdown(string(data))
			if err != nil || skill == nil {
				continue
			}
			if skill.Meta.Name == "" {
				skill.Meta.Name = entry.Name()
			}
			p.mem.AddOrReplace(skill)
		}
	})
}

func (p *FilesystemSkillProvider) Load(ctx context.Context, name string) (*autobuild.Skill, error) {
	p.load()
	return p.mem.Load(ctx, name)
}

func (p *FilesystemSkillProvider) Unload(ctx context.Context, name string) error {
	return p.mem.Unload(ctx, name)
}

func (p *FilesystemSkillProvider) Loaded(ctx context.Context) []string {
	return p.mem.Loaded(ctx)
}

func (p *FilesystemSkillProvider) Match(ctx context.Context, text string) ([]autobuild.SkillMatch, error) {
	p.load()
	return p.mem.Match(ctx, text)
}

func (p *FilesystemSkillProvider) List(ctx context.Context) ([]string, error) {
	p.load()
	return p.mem.List(ctx)
}

func (p *FilesystemSkillProvider) Get(ctx context.Context, name string) (*autobuild.Skill, error) {
	p.load()
	return p.mem.Get(ctx, name)
}

// AddOrReplace replaces a skill at runtime (hot reload).
func (p *FilesystemSkillProvider) AddOrReplace(skill *autobuild.Skill) {
	p.mem.AddOrReplace(skill)
}

// Remove removes a skill at runtime (hot reload).
func (p *FilesystemSkillProvider) Remove(name string) {
	p.mem.Remove(name)
}

// FindDirs searches multiple candidate directories and returns the first that exists.
// Useful when the binary might run from different working directories.
func FindDirs(candidates ...string) string {
	for _, d := range candidates {
		if _, err := os.Stat(d); err == nil {
			return d
		}
	}
	return ""
}

// LoadFromDirs creates a FilesystemSkillProvider from the first existing directory
// among the candidates. Returns an error only if none of the directories exist.
func LoadFromDirs(candidates ...string) (*FilesystemSkillProvider, error) {
	dir := FindDirs(candidates...)
	if dir == "" {
		return nil, fmt.Errorf("no skills directory found among: %s",
			strings.Join(candidates, ", "))
	}
	return NewFilesystem(dir)
}

var _ autobuild.SkillProvider = (*FilesystemSkillProvider)(nil)
