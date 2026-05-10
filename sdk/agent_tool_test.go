package autobuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentMarkdown_FileForm(t *testing.T) {
	root := t.TempDir()
	body := `---
description: "Reviews code for security"
tools:
  - file_read
  - grep
disallowedTools:
  - bash
model: inherit
maxTurns: 5
color: blue
---
You are a security reviewer. Identify vulnerabilities.`
	if err := os.WriteFile(filepath.Join(root, "reviewer.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &FilesystemAgentSource{Root: root}
	list, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d", len(list))
	}
	a := list[0]
	if a.Type != "reviewer" || a.Description != "Reviews code for security" {
		t.Errorf("frontmatter: %+v", a)
	}
	if len(a.Tools) != 2 || a.Tools[0] != "file_read" {
		t.Errorf("tools: %v", a.Tools)
	}
	if len(a.DisallowedTools) != 1 || a.DisallowedTools[0] != "bash" {
		t.Errorf("disallowed: %v", a.DisallowedTools)
	}
	if a.MaxTurns != 5 || a.Color != "blue" {
		t.Errorf("misc: %+v", a)
	}
	if !strings.Contains(a.Body, "security reviewer") {
		t.Errorf("body: %q", a.Body)
	}
}

func TestParseAgentMarkdown_DirForm(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "explorer")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("---\ndescription: \"Explores the codebase\"\n---\nFind relevant files."), 0o644)
	list, _ := (&FilesystemAgentSource{Root: root}).List(context.Background())
	if len(list) != 1 || list[0].Type != "explorer" {
		t.Fatalf("got %#v", list)
	}
}

func TestAgentToolsDescription(t *testing.T) {
	cases := []struct {
		ag   Agent
		want string
	}{
		{Agent{}, "All tools"},
		{Agent{Tools: []string{"a", "b"}}, "a, b"},
		{Agent{DisallowedTools: []string{"bash"}}, "All tools except bash"},
		{Agent{Tools: []string{"a", "b"}, DisallowedTools: []string{"b"}}, "a"},
		{Agent{Tools: []string{"a"}, DisallowedTools: []string{"a"}}, "None"},
	}
	for i, c := range cases {
		if got := agentToolsDesc(&c.ag); got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}

func TestNarrowToolRegistry(t *testing.T) {
	parent := NewToolRegistry()
	parent.Register(&Tool{Name: "alpha"})
	parent.Register(&Tool{Name: "beta"})
	parent.Register(&Tool{Name: "gamma"})

	got := narrowToolRegistry(parent, []string{"alpha", "beta"}, []string{"beta"})
	names := []string{}
	for _, t := range got.List() {
		names = append(names, t.Name)
	}
	if strings.Join(names, ",") != "alpha" {
		t.Errorf("got %v, want [alpha]", names)
	}
}

func TestAgentToolDynamicReminder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"explorer", "reviewer"} {
		body := "---\ndescription: \"" + name + " desc\"\ntools:\n  - file_read\n---\nbody"
		_ = os.WriteFile(filepath.Join(root, name+".md"), []byte(body), 0o644)
	}
	tool := NewAgentTool(AgentToolConfig{
		Sources:      []AgentSource{&FilesystemAgentSource{Root: root}},
		ParentEngine: New(),
		DefaultModel: "test-model",
	})
	r, err := tool.DynamicReminder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r, "explorer") || !strings.Contains(r, "reviewer") {
		t.Errorf("missing agent listing: %s", r)
	}
	if !strings.Contains(r, "Tools: file_read") {
		t.Errorf("tools description not formatted: %s", r)
	}
}

func TestAgentToolIsConcurrencySafe(t *testing.T) {
	tool := NewAgentTool(AgentToolConfig{
		Sources:      []AgentSource{},
		ParentEngine: New(),
	})
	if !tool.IsConcurrencySafe(nil) {
		t.Error("AgentTool must be concurrency-safe so multiple Agent tool_use blocks fan out in parallel")
	}
}

func TestResolveAgentSubagentTypeMatch(t *testing.T) {
	a1 := &Agent{Type: "alpha"}
	a2 := &Agent{Type: "beta"}
	src := &staticAgentSource{agents: []*Agent{a1, a2}}
	got, err := resolveAgent(context.Background(), []AgentSource{src}, nil, "beta")
	if err != nil || got != a2 {
		t.Fatalf("got %v err=%v", got, err)
	}
	got, err = resolveAgent(context.Background(), []AgentSource{src}, nil, "")
	if err != nil || got != a1 {
		t.Fatalf("first-fallback failed got=%v err=%v", got, err)
	}
	_, err = resolveAgent(context.Background(), []AgentSource{src}, nil, "missing")
	if err == nil {
		t.Error("expected error for missing agent type")
	}
}

type staticAgentSource struct{ agents []*Agent }

func (s *staticAgentSource) SourceName() string                              { return "static" }
func (s *staticAgentSource) List(_ context.Context) ([]*Agent, error)        { return s.agents, nil }
