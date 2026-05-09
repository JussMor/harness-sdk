// Package autobuild — SkillTool v3.
//
// Mirrors Claude Code's SkillTool faithfully but stays agnostic:
//   - no provider lock-in (no Anthropic/Claude hardcodes)
//   - no language defaults (no Spanish/English keyword detection)
//   - variables are ${SKILL_DIR} and ${SESSION_ID} (not ${CLAUDE_*})
//   - the caller wires sources, session id, and bash executor
//
// Design (verified against /Users/jussmor/Developer/Claude Code/):
//
//   - Tool name: "Skill". Input: { skill: string, args?: string }.
//   - Skills are NOT loaded into the system prompt. Available skills are
//     surfaced via DynamicReminder as a <system-reminder> attachment, with
//     a 1% character budget over the model's context window.
//   - Layout: <root>/<skill-name>/SKILL.md only. Single .md files in a root
//     are not supported (matches Claude Code's loadSkillsFromSkillsDir).
//   - Lazy: only frontmatter + body are read at startup. Argument
//     substitution, ${SKILL_DIR}/${SESSION_ID} expansion, and bash
//     injection (!`cmd`) all run on each invoke via getPromptForCommand.
//   - Bash injection requires an opt-in BashExecutor and is gated by the
//     skill's allowed-tools whitelist. MCP / remote skills NEVER get bash
//     injection (untrusted).
//
// Cross-reference: see /memories/session/skill-tool-v3-spec.md for the
// full mapping from Claude Code source to this port.
package autobuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ─── Skill model ──────────────────────────────────────────────────────────

// SkillSourceKind identifies where a Skill was loaded from. Used by the
// SkillTool to apply different security policies (e.g. no bash injection
// for remote sources).
type SkillSourceKind string

const (
	SkillSourceBundled    SkillSourceKind = "bundled"
	SkillSourceFilesystem SkillSourceKind = "filesystem"
	SkillSourceRemote     SkillSourceKind = "remote" // MCP-equivalent
)

// Skill is a single invocable capability resolved at startup time.
// The Body is read eagerly; argument substitution and variable expansion
// happen at invoke time, not at load time.
type Skill struct {
	// Identity
	Name        string // canonical name; matches the directory name
	DisplayName string // optional override from frontmatter `name:`
	Description string
	WhenToUse   string

	// Behaviour
	AllowedTools  []string // bash-injection whitelist (allowed-tools frontmatter)
	ArgumentHint  string
	ArgumentNames []string // named placeholders ($foo, $bar, ...)
	Version       string
	Model         string // "" = inherit
	Effort        string // "low" | "medium" | "high" | numeric string | ""

	// Discoverability
	DisableModelInvocation bool // hidden from the SkillTool listing
	UserInvocable          bool // default true; false hides from /commands UI
	Paths                  []string

	// Execution context
	ContextFork bool   // run in a forked subagent (reserved for a future release)
	Agent       string // optional agent override

	// Storage
	Body    string // SKILL.md body (after frontmatter stripping)
	BaseDir string // absolute path of the skill's directory; "" for bundled/remote

	// Provenance
	Source SkillSourceKind
}

// FullDescription returns the listing-friendly "<description> - <whenToUse>"
// string used by the dynamic reminder. Falls back to plain description.
func (s *Skill) FullDescription() string {
	if s.WhenToUse != "" {
		return s.Description + " - " + s.WhenToUse
	}
	return s.Description
}

// SkillSource is a pluggable source of skills. Implementations include
// FilesystemSkillSource (this file), but callers can supply bundled or
// remote sources without modifying the SDK.
type SkillSource interface {
	// SourceName is a stable label used for diagnostics and dedup.
	SourceName() string
	// List returns all skills currently exposed by this source. Called once
	// per turn (cached by SkillTool); cheap implementations are fine.
	List(ctx context.Context) ([]*Skill, error)
}

// ─── Filesystem source ────────────────────────────────────────────────────

// FilesystemSkillSource scans `<Root>/<skill-name>/SKILL.md` directory
// layouts. Mirrors Claude Code's loadSkillsFromSkillsDir.
type FilesystemSkillSource struct {
	Root  string
	Label string          // optional override for SourceName()
	Kind  SkillSourceKind // defaults to SkillSourceFilesystem

	mu     sync.Mutex
	cached []*Skill
	loaded bool
}

func (f *FilesystemSkillSource) SourceName() string {
	if f.Label != "" {
		return f.Label
	}
	return "fs:" + f.Root
}

func (f *FilesystemSkillSource) Reload() {
	f.mu.Lock()
	f.loaded = false
	f.cached = nil
	f.mu.Unlock()
}

func (f *FilesystemSkillSource) List(_ context.Context) ([]*Skill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loaded {
		return f.cached, nil
	}

	kind := f.Kind
	if kind == "" {
		kind = SkillSourceFilesystem
	}

	entries, err := os.ReadDir(f.Root)
	if err != nil {
		if os.IsNotExist(err) {
			f.loaded = true
			return nil, nil
		}
		return nil, fmt.Errorf("skills dir %s: %w", f.Root, err)
	}

	var out []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue // single .md files in /skills/ NOT supported
		}
		dir := filepath.Join(f.Root, e.Name())
		skillPath := filepath.Join(dir, "SKILL.md")
		raw, err := os.ReadFile(skillPath)
		if err != nil {
			continue // missing SKILL.md → silently skip
		}
		skill, err := parseSkillMarkdown(e.Name(), dir, string(raw), kind)
		if err != nil {
			continue
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	f.cached = out
	f.loaded = true
	return out, nil
}

// parseSkillMarkdown applies the existing parseFrontmatter + the Skill-
// specific frontmatter mapping.
func parseSkillMarkdown(name, baseDir, raw string, kind SkillSourceKind) (*Skill, error) {
	fields, lists, body, err := parseFrontmatter(raw)
	if err != nil {
		// No frontmatter is allowed; treat the whole file as body and use
		// the directory name as the only identity.
		return &Skill{
			Name:          name,
			Body:          raw,
			BaseDir:       baseDir,
			Source:        kind,
			UserInvocable: true,
		}, nil
	}

	stripQuotes := func(s string) string {
		if len(s) >= 2 {
			if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
				return s[1 : len(s)-1]
			}
		}
		return s
	}

	allowed := lists["allowed-tools"]
	if v, ok := fields["allowed-tools"]; ok && len(allowed) == 0 {
		for _, p := range strings.Split(stripQuotes(v), ",") {
			if t := strings.TrimSpace(p); t != "" {
				allowed = append(allowed, t)
			}
		}
	}

	argNames := lists["arguments"]
	if v, ok := fields["arguments"]; ok && len(argNames) == 0 {
		for _, p := range strings.Fields(stripQuotes(v)) {
			argNames = append(argNames, p)
		}
	}

	paths := lists["paths"]
	if v, ok := fields["paths"]; ok && len(paths) == 0 {
		for _, p := range strings.Split(stripQuotes(v), ",") {
			if t := strings.TrimSpace(p); t != "" {
				paths = append(paths, t)
			}
		}
	}

	parseBool := func(s string) bool {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1", "on":
			return true
		default:
			return false
		}
	}

	skill := &Skill{
		Name:                   name,
		DisplayName:            stripQuotes(fields["name"]),
		Description:            stripQuotes(fields["description"]),
		WhenToUse:              stripQuotes(fields["when_to_use"]),
		AllowedTools:           allowed,
		ArgumentHint:           stripQuotes(fields["argument-hint"]),
		ArgumentNames:          argNames,
		Version:                stripQuotes(fields["version"]),
		Model:                  stripQuotes(fields["model"]),
		Effort:                 stripQuotes(fields["effort"]),
		DisableModelInvocation: parseBool(fields["disable-model-invocation"]),
		UserInvocable:          true,
		Paths:                  paths,
		ContextFork:            stripQuotes(fields["context"]) == "fork",
		Agent:                  stripQuotes(fields["agent"]),
		Body:                   body,
		BaseDir:                baseDir,
		Source:                 kind,
	}
	if v, ok := fields["user-invocable"]; ok {
		skill.UserInvocable = parseBool(v)
	}
	if skill.DisplayName == "" {
		skill.DisplayName = name
	}
	return skill, nil
}

// ─── Argument substitution (port of utils/argumentSubstitution.ts) ────────

// parseSkillArguments splits a raw args string into tokens, honouring
// double and single quotes. Backslash escapes inside double quotes work.
// This is a deliberately small subset of POSIX shell quoting; it covers
// the cases Claude Code's shell-quote handles in practice.
func parseSkillArguments(args string) []string {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inSingle, inDouble, escape := false, false, false
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case escape:
			cur.WriteByte(c)
			escape = false
		case c == '\\' && inDouble:
			escape = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case (c == ' ' || c == '\t') && !inSingle && !inDouble:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var (
	indexedArgRe = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	shorthandRe  = regexp.MustCompile(`\$(\d+)([^\w]|$)`)
)

// substituteSkillArguments applies the same substitution order as
// Claude Code: named → $ARGUMENTS[N] → $N → $ARGUMENTS → trailing append.
func substituteSkillArguments(content string, args string, argumentNames []string, appendIfNoPlaceholder bool) string {
	original := content
	parsed := parseSkillArguments(args)

	for i, name := range argumentNames {
		if name == "" {
			continue
		}
		pattern := regexp.MustCompile(`\$` + regexp.QuoteMeta(name) + `(?:[^\w\[]|$)`)
		val := ""
		if i < len(parsed) {
			val = parsed[i]
		}
		content = pattern.ReplaceAllStringFunc(content, func(m string) string {
			// keep the trailing boundary char that the lookahead consumed
			tail := m[1+len(name):]
			return val + tail
		})
	}

	content = indexedArgRe.ReplaceAllStringFunc(content, func(m string) string {
		sm := indexedArgRe.FindStringSubmatch(m)
		if len(sm) < 2 {
			return ""
		}
		var idx int
		_, _ = fmt.Sscanf(sm[1], "%d", &idx)
		if idx >= 0 && idx < len(parsed) {
			return parsed[idx]
		}
		return ""
	})

	content = shorthandRe.ReplaceAllStringFunc(content, func(m string) string {
		sm := shorthandRe.FindStringSubmatch(m)
		if len(sm) < 3 {
			return ""
		}
		var idx int
		_, _ = fmt.Sscanf(sm[1], "%d", &idx)
		val := ""
		if idx >= 0 && idx < len(parsed) {
			val = parsed[idx]
		}
		return val + sm[2]
	})

	content = strings.ReplaceAll(content, "$ARGUMENTS", args)

	if content == original && appendIfNoPlaceholder && args != "" {
		content += "\n\nARGUMENTS: " + args
	}
	return content
}

// ─── Listing budget (port of tools/SkillTool/prompt.ts) ───────────────────

// Skill listing constants (mirror Claude Code).
const (
	SkillBudgetContextPercent = 0.01
	SkillCharsPerToken        = 4
	SkillDefaultCharBudget    = 8_000
	SkillMaxListingDescChars  = 250
	SkillMinDescLen           = 20
)

// SkillListingCharBudget computes the listing budget in characters from
// the model's context window in tokens. Falls back to 8 000 chars.
func SkillListingCharBudget(contextWindowTokens int) int {
	if contextWindowTokens <= 0 {
		return SkillDefaultCharBudget
	}
	return int(float64(contextWindowTokens) * SkillCharsPerToken * SkillBudgetContextPercent)
}

// formatSkillListingEntry returns "- <name>: <desc>" with the description
// truncated to SkillMaxListingDescChars (suffix "…", single rune).
func formatSkillListingEntry(s *Skill) string {
	desc := s.FullDescription()
	if len(desc) > SkillMaxListingDescChars {
		desc = desc[:SkillMaxListingDescChars-1] + "…"
	}
	return "- " + s.Name + ": " + desc
}

// FormatSkillsWithinBudget renders skill listing under a char budget,
// degrading gracefully: full → trimmed-rest → names-only-rest. Bundled
// skills are NEVER truncated (matches Claude Code).
func FormatSkillsWithinBudget(skills []*Skill, budget int) string {
	if len(skills) == 0 {
		return ""
	}
	if budget <= 0 {
		budget = SkillDefaultCharBudget
	}

	full := make([]string, len(skills))
	total := 0
	for i, s := range skills {
		full[i] = formatSkillListingEntry(s)
		total += len(full[i])
	}
	total += len(skills) - 1 // join newlines
	if total <= budget {
		return strings.Join(full, "\n")
	}

	bundledIdx := make(map[int]bool)
	var rest []*Skill
	for i, s := range skills {
		if s.Source == SkillSourceBundled {
			bundledIdx[i] = true
		} else {
			rest = append(rest, s)
		}
	}

	bundledChars := 0
	for i := range skills {
		if bundledIdx[i] {
			bundledChars += len(full[i]) + 1
		}
	}
	remaining := budget - bundledChars

	if len(rest) == 0 {
		return strings.Join(full, "\n")
	}

	nameOverhead := 0
	for _, s := range rest {
		nameOverhead += len(s.Name) + 4 // "- " + ": "
	}
	nameOverhead += len(rest) - 1
	avail := remaining - nameOverhead
	maxDescLen := avail / len(rest)

	out := make([]string, len(skills))
	if maxDescLen < SkillMinDescLen {
		// names-only fallback (bundled keep full)
		for i, s := range skills {
			if bundledIdx[i] {
				out[i] = full[i]
			} else {
				out[i] = "- " + s.Name
			}
		}
		return strings.Join(out, "\n")
	}
	for i, s := range skills {
		if bundledIdx[i] {
			out[i] = full[i]
			continue
		}
		desc := s.FullDescription()
		if len(desc) > maxDescLen {
			if maxDescLen > 1 {
				desc = desc[:maxDescLen-1] + "…"
			} else {
				desc = "…"
			}
		}
		out[i] = "- " + s.Name + ": " + desc
	}
	return strings.Join(out, "\n")
}

// ─── Bash injection hook (caller-supplied, opt-in) ───────────────────────

// SkillBashExecutor lets callers wire bash-injection (!`cmd` syntax in
// skill bodies) to whatever sandbox they use. It is opt-in: when nil,
// !`cmd` blocks are left as-is in the rendered prompt and the model sees
// them verbatim.
//
// The SDK guarantees executor is NEVER invoked for SkillSourceRemote
// skills (untrusted). For local/bundled skills, the SDK passes the
// allowed-tools list so the executor can enforce the whitelist.
type SkillBashExecutor interface {
	Execute(ctx context.Context, command string, allowedTools []string) (string, error)
}

// bashInjectRe matches `!`...`` (single backtick form) and triple-backtick
// `! ...` blocks. Subset of Claude Code's executeShellCommandsInPrompt.
var bashInjectRe = regexp.MustCompile("!`([^`]+)`")

func runSkillBashInjection(ctx context.Context, body string, allowed []string, exec SkillBashExecutor) string {
	if exec == nil {
		return body
	}
	return bashInjectRe.ReplaceAllStringFunc(body, func(m string) string {
		sm := bashInjectRe.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		out, err := exec.Execute(ctx, sm[1], allowed)
		if err != nil {
			return fmt.Sprintf("[bash error: %s]", err)
		}
		return strings.TrimRight(out, "\n")
	})
}

// ─── SkillTool factory ────────────────────────────────────────────────────

// SkillToolConfig wires a SkillTool. All fields are agnostic — no
// provider-specific or language-specific defaults.
type SkillToolConfig struct {
	// Sources discover skills. Order matters: earlier sources win on name
	// collisions (matches Claude Code's uniqBy(localCommands, mcpSkills)).
	Sources []SkillSource

	// SessionIDFn supplies the current session ID for ${SESSION_ID}
	// expansion. May be nil (variable then expands to "").
	SessionIDFn func() string

	// BashExecutor enables !`cmd` injection. May be nil.
	BashExecutor SkillBashExecutor

	// ContextWindowTokens is used to compute the dynamic listing budget.
	// 0 → use SkillDefaultCharBudget.
	ContextWindowTokens int
}

// NewSkillTool returns a *Tool that exposes skill discovery and invocation
// to the LLM. The tool's DynamicReminder hook surfaces available skills
// in a <system-reminder> on every turn (within a 1% character budget).
func NewSkillTool(cfg SkillToolConfig) *Tool {
	return &Tool{
		Name:        "Skill",
		Description: skillToolPrompt,
		Category:    ToolCategoryPlanning,
		IsReadOnly:  func(map[string]any) bool { return true },
		Parameters: ToolFuncParams{
			Type: "object",
			Properties: map[string]ToolParam{
				"skill": {
					Type:        "string",
					Description: "The exact name of the skill to invoke (e.g. \"pdf\", \"commit\"). Must match a name from the system-reminder skill listing.",
				},
				"args": {
					Type:        "string",
					Description: "Optional arguments string passed to the skill. Use shell-style quoting for multi-word args.",
				},
			},
			Required: []string{"skill"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			name, _ := args["skill"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return "", fmt.Errorf("skill: missing required arg 'skill'")
			}
			rawArgs, _ := args["args"].(string)

			skill, err := resolveSkill(ctx, cfg.Sources, name)
			if err != nil {
				return "", err
			}
			if skill.DisableModelInvocation {
				return "", fmt.Errorf("skill %q has disable-model-invocation set", name)
			}

			rendered := skill.Body
			if skill.BaseDir != "" {
				rendered = "Base directory for this skill: " + skill.BaseDir + "\n\n" + rendered
			}
			rendered = substituteSkillArguments(rendered, rawArgs, skill.ArgumentNames, true)

			// ${SKILL_DIR}: forward-slashed for shell safety.
			if skill.BaseDir != "" {
				dir := filepath.ToSlash(skill.BaseDir)
				rendered = strings.ReplaceAll(rendered, "${SKILL_DIR}", dir)
			}
			// ${SESSION_ID}: caller-supplied; never blanks out if Fn is nil.
			sid := ""
			if cfg.SessionIDFn != nil {
				sid = cfg.SessionIDFn()
			}
			rendered = strings.ReplaceAll(rendered, "${SESSION_ID}", sid)

			// Bash injection — disabled for remote/MCP skills always.
			if skill.Source != SkillSourceRemote {
				rendered = runSkillBashInjection(ctx, rendered, skill.AllowedTools, cfg.BashExecutor)
			}
			return rendered, nil
		},
		DynamicReminder: func(ctx context.Context) (string, error) {
			skills, err := collectSkills(ctx, cfg.Sources)
			if err != nil || len(skills) == 0 {
				return "", nil
			}
			visible := make([]*Skill, 0, len(skills))
			for _, s := range skills {
				if s.DisableModelInvocation {
					continue
				}
				visible = append(visible, s)
			}
			if len(visible) == 0 {
				return "", nil
			}
			budget := SkillListingCharBudget(cfg.ContextWindowTokens)
			body := "Available skills (invoke via the Skill tool):\n" +
				FormatSkillsWithinBudget(visible, budget)
			return body, nil
		},
	}
}

// resolveSkill returns the first skill across sources matching name.
// Earlier sources win on collisions.
func resolveSkill(ctx context.Context, sources []SkillSource, name string) (*Skill, error) {
	for _, src := range sources {
		list, err := src.List(ctx)
		if err != nil {
			continue
		}
		for _, s := range list {
			if s.Name == name {
				return s, nil
			}
		}
	}
	return nil, fmt.Errorf("skill %q not found", name)
}

// collectSkills merges all sources, dropping duplicates by name (first wins).
func collectSkills(ctx context.Context, sources []SkillSource) ([]*Skill, error) {
	var out []*Skill
	seen := make(map[string]bool)
	for _, src := range sources {
		list, err := src.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// skillToolPrompt is the (static) tool description the LLM sees. The
// dynamic skill listing rides as a system-reminder, NOT in this string —
// putting per-turn data in the description breaks Anthropic prompt cache.
const skillToolPrompt = `Execute a skill within the main conversation.

Skills provide specialized capabilities and domain knowledge. When the user's request matches an available skill, invoke it BEFORE generating any other response about the task.

How to invoke:
- Provide the skill name and optional arguments string.
- Examples:
  - skill: "pdf"
  - skill: "commit", args: "-m 'Fix bug'"
  - skill: "review-pr", args: "123"

Important:
- Available skills are listed in the <system-reminder> attached to this turn.
- NEVER mention a skill without actually calling this tool.
- Do not invoke a skill that is already running.
- If a skill body has been included in this turn already, follow it directly without re-invoking.`
