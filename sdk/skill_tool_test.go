package autobuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillArguments(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"foo bar baz", []string{"foo", "bar", "baz"}},
		{`foo "hello world" baz`, []string{"foo", "hello world", "baz"}},
		{`foo 'hello world' baz`, []string{"foo", "hello world", "baz"}},
		{"   ", nil},
		{"", nil},
		{`a "b\"c" d`, []string{"a", `b"c`, "d"}},
	}
	for _, c := range cases {
		got := parseSkillArguments(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("parseSkillArguments(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSubstituteSkillArguments(t *testing.T) {
	t.Run("named args", func(t *testing.T) {
		out := substituteSkillArguments("hello $name from $place", "alice paris", []string{"name", "place"}, true)
		if out != "hello alice from paris" {
			t.Errorf("got %q", out)
		}
	})
	t.Run("indexed", func(t *testing.T) {
		out := substituteSkillArguments("$ARGUMENTS[0] then $ARGUMENTS[1]", "a b", nil, true)
		if out != "a then b" {
			t.Errorf("got %q", out)
		}
	})
	t.Run("shorthand", func(t *testing.T) {
		out := substituteSkillArguments("first=$0 second=$1.", "a b", nil, true)
		if out != "first=a second=b." {
			t.Errorf("got %q", out)
		}
	})
	t.Run("$ARGUMENTS literal", func(t *testing.T) {
		out := substituteSkillArguments("run with $ARGUMENTS", "x y z", nil, true)
		if out != "run with x y z" {
			t.Errorf("got %q", out)
		}
	})
	t.Run("appendIfNoPlaceholder", func(t *testing.T) {
		out := substituteSkillArguments("just text", "extra", nil, true)
		if !strings.HasSuffix(out, "ARGUMENTS: extra") {
			t.Errorf("expected appended ARGUMENTS, got %q", out)
		}
	})
	t.Run("no append when empty args", func(t *testing.T) {
		out := substituteSkillArguments("just text", "", nil, true)
		if out != "just text" {
			t.Errorf("got %q", out)
		}
	})
}

func TestFormatSkillsWithinBudget(t *testing.T) {
	skills := []*Skill{
		{Name: "a", Description: "first skill"},
		{Name: "b", Description: "second skill", WhenToUse: "when reasonable"},
	}
	out := FormatSkillsWithinBudget(skills, 1000)
	if !strings.Contains(out, "- a: first skill") || !strings.Contains(out, "- b: second skill - when reasonable") {
		t.Fatalf("unexpected listing:\n%s", out)
	}
}

func TestFormatSkillsWithinBudgetNamesOnlyFallback(t *testing.T) {
	var skills []*Skill
	for i := 0; i < 50; i++ {
		skills = append(skills, &Skill{
			Name:        "skill" + string(rune('a'+i%26)),
			Description: strings.Repeat("x", 200),
		})
	}
	out := FormatSkillsWithinBudget(skills, 200)
	// names-only fallback: each line is "- skillX"
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "- skill") {
			t.Fatalf("expected names-only, got line %q", line)
		}
	}
}

func TestFilesystemSkillSource(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
description: "Demo skill"
when_to_use: "When testing"
arguments: foo bar
allowed-tools:
  - bash
---
Hello $foo and $bar from ${SKILL_DIR} session ${SESSION_ID}.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	src := &FilesystemSkillSource{Root: root}
	skills, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "demo-skill" {
		t.Fatalf("got %#v", skills)
	}
	s := skills[0]
	if s.Description != "Demo skill" || s.WhenToUse != "When testing" {
		t.Errorf("frontmatter mismatch: %+v", s)
	}
	if len(s.AllowedTools) != 1 || s.AllowedTools[0] != "bash" {
		t.Errorf("allowed-tools: %+v", s.AllowedTools)
	}
	if len(s.ArgumentNames) != 2 {
		t.Errorf("arg names: %+v", s.ArgumentNames)
	}
}

func TestSkillToolExecute(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "greet")
	_ = os.MkdirAll(skillDir, 0o755)
	body := `---
description: "Greet someone"
arguments: name
---
Hello $name from ${SKILL_DIR} (session=${SESSION_ID}).`
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644)

	tool := NewSkillTool(SkillToolConfig{
		Sources:     []SkillSource{&FilesystemSkillSource{Root: root}},
		SessionIDFn: func() string { return "sess-42" },
	})

	out, err := tool.Execute(context.Background(), "", map[string]any{
		"skill": "greet",
		"args":  "Alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Hello Alice") {
		t.Errorf("missing greeting: %s", out)
	}
	if !strings.Contains(out, "session=sess-42") {
		t.Errorf("session not substituted: %s", out)
	}
	if !strings.Contains(out, filepath.ToSlash(skillDir)) {
		t.Errorf("SKILL_DIR not substituted: %s", out)
	}
	if !strings.Contains(out, "Base directory for this skill") {
		t.Errorf("missing base directory header")
	}
}

func TestSkillToolDynamicReminder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, name)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: \""+name+" desc\"\n---\nbody"), 0o644)
	}
	tool := NewSkillTool(SkillToolConfig{
		Sources: []SkillSource{&FilesystemSkillSource{Root: root}},
	})
	reminder, err := tool.DynamicReminder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reminder, "alpha") || !strings.Contains(reminder, "beta") {
		t.Errorf("reminder missing skills: %s", reminder)
	}
}

func TestSkillToolDisableModelInvocation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secret")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: secret\ndisable-model-invocation: true\n---\nbody"), 0o644)

	tool := NewSkillTool(SkillToolConfig{
		Sources: []SkillSource{&FilesystemSkillSource{Root: root}},
	})

	// Listing should hide it
	r, _ := tool.DynamicReminder(context.Background())
	if strings.Contains(r, "secret") {
		t.Errorf("disable-model-invocation skill leaked into listing: %s", r)
	}
	// Direct call should fail
	_, err := tool.Execute(context.Background(), "", map[string]any{"skill": "secret"})
	if err == nil {
		t.Error("expected error invoking disabled skill")
	}
}

func TestSkillToolBashInjectionGated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "remote-thing")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: remote\n---\nResult: !`echo hi`"), 0o644)

	called := false
	exec := skillBashExecutorFunc(func(_ context.Context, cmd string, _ []string) (string, error) {
		called = true
		return "OK", nil
	})

	// Mark source as remote — must NOT call executor.
	src := &FilesystemSkillSource{Root: root, Kind: SkillSourceRemote}
	tool := NewSkillTool(SkillToolConfig{Sources: []SkillSource{src}, BashExecutor: exec})
	out, err := tool.Execute(context.Background(), "", map[string]any{"skill": "remote-thing"})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("bash executor was called for remote skill — must be gated")
	}
	if !strings.Contains(out, "!`echo hi`") {
		t.Errorf("expected literal injection in remote output, got: %s", out)
	}

	// Same skill, filesystem source — executor must run.
	src2 := &FilesystemSkillSource{Root: root, Kind: SkillSourceFilesystem}
	tool2 := NewSkillTool(SkillToolConfig{Sources: []SkillSource{src2}, BashExecutor: exec})
	called = false
	out2, err := tool2.Execute(context.Background(), "", map[string]any{"skill": "remote-thing"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("expected executor call for filesystem skill")
	}
	if !strings.Contains(out2, "Result: OK") {
		t.Errorf("unexpected substitution: %s", out2)
	}
}

type skillBashExecutorFunc func(context.Context, string, []string) (string, error)

func (f skillBashExecutorFunc) Execute(ctx context.Context, cmd string, allowed []string) (string, error) {
	return f(ctx, cmd, allowed)
}
