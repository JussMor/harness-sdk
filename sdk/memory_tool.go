package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════
// memory_tool — typed-memory tool aligned with Claude Code's memdir/
// ═══════════════════════════════════════════════════════════════════════
//
// NewMemoryTool returns a single Tool exposing the full memdir contract:
//
//   - Closed taxonomy: {user, feedback, project, reference}
//   - Frontmatter-driven create (name + description + type)
//   - Read-before-write enforcement (str_replace requires prior view)
//   - Anti-merge guard: refuse to write content of one type into a file
//     whose existing frontmatter declares a different type
//   - find_relevant operation that scans + ranks via the configured
//     MemorySelector and returns content with age annotations
//   - View output is annotated with relative age and a freshness note
//     when older than 1 day
//
// The tool is deliberately a single multi-operation tool rather than 10
// micro-tools: it matches Claude Code's Memory tool footprint and keeps
// the model's tool surface compact.

// MemoryToolConfig configures NewMemoryTool.
type MemoryToolConfig struct {
	// Provider is the backing MemoryProvider. Required.
	Provider MemoryProvider

	// Selector picks relevant memories for `find_relevant`.
	// Defaults to KeywordMemorySelector (no LLM call).
	Selector MemorySelector

	// Tracker enforces the read-before-write contract.
	// If nil, a fresh in-memory tracker is created per tool instance.
	Tracker *ReadBeforeWriteTracker

	// DefaultScope is used when the model omits the `scope` argument.
	// Defaults to ScopeProject.
	DefaultScope Scope

	// AllowedScopes limits which scopes the model can write to. nil =
	// {ScopeUser, ScopeProject}. Reads always allow either scope.
	AllowedScopes []Scope

	// DisableReadBeforeWrite turns the contract off (default: enforced).
	// Disable only for tests / scripted seeding.
	DisableReadBeforeWrite bool

	// DisableTaxonomy allows create/str_replace without a `type` argument
	// (default: required).
	DisableTaxonomy bool

	// DisableAntiMerge skips type-mismatch protection on writes (default: on).
	DisableAntiMerge bool

	// FindRelevantLimit caps `find_relevant` result count. Defaults to 5.
	FindRelevantLimit int
}

func (c *MemoryToolConfig) defaults() {
	if c.DefaultScope == "" {
		c.DefaultScope = ScopeProject
	}
	if len(c.AllowedScopes) == 0 {
		c.AllowedScopes = []Scope{ScopeUser, ScopeProject}
	}
	if c.Selector == nil {
		c.Selector = KeywordMemorySelector{}
	}
	if c.Tracker == nil {
		c.Tracker = NewReadBeforeWriteTracker()
	}
	if c.FindRelevantLimit <= 0 {
		c.FindRelevantLimit = 5
	}
}

const memoryToolPrompt = `Persistent typed-memory store. **Use the memory tool — do NOT shell out to the filesystem; memory is host-managed and not visible under any directory you can list with bash/find.**

Memories are constrained to four types capturing context NOT derivable from the
current project state:

  - user      — the user's role, goals, preferences, knowledge
  - feedback  — guidance the user gave about how to approach work (corrections
                AND validated approaches); include **Why:** and **How to apply:**
  - project   — ongoing work, deadlines, motivations not in the code/git
  - reference — pointers to external systems (Linear, Grafana, Slack, etc.)

DO NOT save: code patterns, architecture, file paths, git history, or anything
already in CLAUDE.md / a SKILL.md.

Contract:
  - Always supply 'path' for any operation that targets a file. Paths look
    like "/preferences.md" or "/feedback/testing.md" — leading slash, no scope.
  - Always 'view' or 'list' before 'str_replace' / 'delete' / 'rename'.
  - Each memory is its own file with frontmatter (name, description, type).
  - Never merge content of one type into a file of a different type — create
    a new file instead.
  - After 'create', also append a one-line pointer to the scope's
    /MEMORY.md index: "- [Title](file.md) — one-line hook".

Operations: view, create, str_replace, delete, rename, list, search,
find_relevant.`

// NewMemoryTool builds the memdir-aligned memory tool.
func NewMemoryTool(cfg MemoryToolConfig) *Tool {
	if cfg.Provider == nil {
		// Caller error; surface at registration time via a stub that errors.
		return &Tool{
			Name:        "memory",
			Description: "memory tool not configured (no provider)",
			Category:    ToolCategoryMemory,
			Execute: func(context.Context, string, map[string]any) (string, error) {
				return "memory provider not configured", nil
			},
		}
	}
	cfg.defaults()

	allowed := map[Scope]bool{}
	for _, s := range cfg.AllowedScopes {
		allowed[s] = true
	}

	return &Tool{
		Name:        "memory",
		Description: memoryToolPrompt,
		Category:    ToolCategoryMemory,
		Aliases:     []string{"memory-operations"},
		Parameters: ToolFuncParams{
			Type: "object",
			Properties: map[string]ToolParam{
				"operation": {
					Type:        "string",
					Description: "view | create | str_replace | delete | rename | list | search | find_relevant",
					Enum:        []string{"view", "create", "str_replace", "delete", "rename", "list", "search", "find_relevant"},
				},
				"scope": {
					Type:        "string",
					Description: "user | project (defaults to project; reads also accept '*')",
					Enum:        []string{"user", "project", "*"},
				},
				"path":        {Type: "string", Description: "Memory file path (e.g. /feedback/testing.md)."},
				"content":     {Type: "string", Description: "Body content for 'create'. Frontmatter is auto-added when name/description/type are provided."},
				"name":        {Type: "string", Description: "Frontmatter `name:` for 'create'."},
				"description": {Type: "string", Description: "Frontmatter `description:` (one line, used for relevance selection)."},
				"type":        {Type: "string", Description: "Memory taxonomy type.", Enum: []string{"user", "feedback", "project", "reference"}},
				"oldStr":      {Type: "string", Description: "For 'str_replace': exact existing substring to replace (must be unique)."},
				"newStr":      {Type: "string", Description: "For 'str_replace': replacement text."},
				"oldPath":     {Type: "string", Description: "For 'rename': source path."},
				"newPath":     {Type: "string", Description: "For 'rename': destination path."},
				"query":       {Type: "string", Description: "For 'search' / 'find_relevant'."},
			},
			Required: []string{"operation"},
		},
		IsReadOnly: func(args map[string]any) bool {
			op := strings.ToLower(strings.TrimSpace(asMemString(args["operation"])))
			switch op {
			case "view", "list", "search", "find_relevant":
				return true
			}
			return false
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			return executeMemoryTool(ctx, &cfg, allowed, args)
		},
		// DynamicReminder surfaces the available memory manifest on every
		// turn as a <system-reminder>. Mirrors Claude Code's behaviour where
		// the LLM sees a per-turn index of memory files (typed, dated,
		// described) and is reminded to read them on demand via memory.view
		// rather than guessing from the system prompt alone.
		DynamicReminder: func(ctx context.Context) (string, error) {
			return buildMemoryReminder(ctx, &cfg)
		},
	}
}

// buildMemoryReminder collects the manifest of memory files across all
// allowed scopes and renders the per-turn reminder body. Returns "" when
// memory is completely empty so no <system-reminder> wrapper is emitted.
func buildMemoryReminder(ctx context.Context, cfg *MemoryToolConfig) (string, error) {
	if cfg.Provider == nil {
		return "", nil
	}
	scopes := cfg.AllowedScopes
	if len(scopes) == 0 {
		scopes = []Scope{ScopeUser, ScopeProject}
	}
	var (
		entries  []MemoryHeader
		indexes  []string // MEMORY.md content per scope, headed with scope label
	)
	for _, s := range scopes {
		// Scan every typed file under the scope root.
		hs, _ := ScanMemoryFiles(ctx, cfg.Provider, s, "/")
		entries = append(entries, hs...)

		// Bootstrap MEMORY.md (the index). Loaded eagerly because it's
		// short by contract and pointers here drive the rest.
		if body, err := cfg.Provider.View(ctx, s, "/MEMORY.md"); err == nil {
			body = strings.TrimSpace(body)
			if body != "" {
				indexes = append(indexes,
					"### MEMORY.md ("+string(s)+" scope)\n\n"+body)
			}
		}
	}
	if len(entries) == 0 && len(indexes) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("# Memory\n\n")
	b.WriteString("Persistent memory files are available. Read individual files on demand via `memory(operation: \"view\", path: \"…\", scope: \"…\")`. Do not assume contents — files older than a day may be stale.\n")

	if len(indexes) > 0 {
		b.WriteString("\n## Index\n\n")
		b.WriteString(strings.Join(indexes, "\n\n"))
		b.WriteString("\n")
	}

	if len(entries) > 0 {
		b.WriteString("\n## Available memory files\n\n")
		b.WriteString(FormatMemoryManifest(entries))
	}

	b.WriteString("\nIf the user asks something about themselves or past decisions, check this manifest before answering. Save new facts via `memory(operation: \"create\", …)` with the appropriate `type`.")
	return b.String(), nil
}

// ── execution ───────────────────────────────────────────────────────────────

func executeMemoryTool(ctx context.Context, cfg *MemoryToolConfig, allowed map[Scope]bool, args map[string]any) (string, error) {
	op := strings.ToLower(strings.TrimSpace(asMemString(args["operation"])))
	scope := parseToolScope(asMemString(args["scope"]), cfg.DefaultScope)
	path := asMemString(args["path"])

	stater, _ := cfg.Provider.(MemoryStater)
	statMtime := func(s Scope, p string) int64 {
		if stater == nil {
			return 0
		}
		st, err := stater.Stat(ctx, s, p)
		if err != nil {
			return 0
		}
		return st.MtimeMs
	}

	switch op {
	case "view":
		content, err := cfg.Provider.View(ctx, scope, path)
		if err != nil {
			return "", err
		}
		mtime := statMtime(scope, path)
		cfg.Tracker.MarkRead(scope, path, mtime)
		if content == "" {
			return "(empty)", nil
		}
		out := content
		if note := MemoryFreshnessNote(mtime); note != "" {
			out = note + out
		}
		if mtime > 0 {
			out = fmt.Sprintf("(last modified: %s)\n", MemoryAge(mtime)) + out
		}
		return out, nil

	case "list":
		paths, err := cfg.Provider.List(ctx, scope, path)
		if err != nil {
			return "", err
		}
		return strings.Join(paths, "\n"), nil

	case "search":
		entries, err := cfg.Provider.Search(ctx, scope, asMemString(args["query"]))
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&b, "- [%s] %s\n", e.Scope, e.Path)
		}
		return b.String(), nil

	case "find_relevant":
		scopes := []Scope{}
		if scope == "*" || scope == "" {
			scopes = nil
		} else {
			scopes = []Scope{scope}
		}
		results, err := FindRelevantMemories(ctx, cfg.Provider, asMemString(args["query"]), FindRelevantMemoriesOptions{
			Scopes:   scopes,
			Limit:    cfg.FindRelevantLimit,
			Selector: cfg.Selector,
		})
		if err != nil {
			return "", err
		}
		if len(results) == 0 {
			return "(no relevant memories)", nil
		}
		var b strings.Builder
		for _, r := range results {
			fmt.Fprintf(&b, "── [%s] %s (%s) ──\n", r.Header.Type, r.Header.Path, MemoryAge(r.Header.MtimeMs))
			if note := MemoryFreshnessNote(r.Header.MtimeMs); note != "" {
				b.WriteString(note)
			}
			b.WriteString(r.Content)
			b.WriteString("\n\n")
			cfg.Tracker.MarkRead(r.Header.Scope, r.Header.Path, r.Header.MtimeMs)
		}
		return b.String(), nil

	case "create":
		if !allowed[scope] {
			return "", fmt.Errorf("scope %q is not writable", scope)
		}
		if path == "" || strings.HasSuffix(path, "/") {
			return "", fmt.Errorf("'path' is required and must point to a file (e.g. \"/preferences.md\")")
		}
		mt := ParseMemoryType(asMemString(args["type"]))
		if !cfg.DisableTaxonomy && mt == "" {
			return "", fmt.Errorf("'type' is required and must be one of: %s", joinMemoryTypes())
		}
		// Anti-merge: if the file already exists, refuse — caller should pick a
		// different filename rather than reusing one of a different type.
		// stat first so we don't confuse an existing directory listing (View
		// returns content for dirs too) with an actual file collision.
		exists := false
		if stater != nil {
			if st, err := stater.Stat(ctx, scope, path); err == nil && !st.IsDir {
				exists = true
			}
		} else {
			// No stater — fall back to View. Reasonable for filesystem
			// providers but may produce false positives for directories.
			if c, err := cfg.Provider.View(ctx, scope, path); err == nil && c != "" {
				exists = true
			}
		}
		if exists {
			if existing, err := cfg.Provider.View(ctx, scope, path); err == nil && existing != "" {
				fm, _ := ParseMemoryFrontmatter(existing)
				if !cfg.DisableAntiMerge && fm.Type != "" && mt != "" && fm.Type != mt {
					return "", fmt.Errorf("anti-merge: %s is type=%s; cannot create %s content here. Use a separate file.", path, fm.Type, mt)
				}
				return "", fmt.Errorf("memory %s already exists; use str_replace or rename", path)
			}
		}
		body := asMemString(args["content"])
		fm := MemoryFrontmatter{
			Name:        asMemString(args["name"]),
			Description: asMemString(args["description"]),
			Type:        mt,
		}
		full := fm.Render() + body
		if err := cfg.Provider.Create(ctx, scope, path, full); err != nil {
			return "", err
		}
		cfg.Tracker.MarkRead(scope, path, statMtime(scope, path))
		return fmt.Sprintf("created %s [%s]", path, mt), nil

	case "str_replace":
		if !allowed[scope] {
			return "", fmt.Errorf("scope %q is not writable", scope)
		}
		if path == "" {
			return "", fmt.Errorf("'path' is required (e.g. \"/preferences.md\")")
		}
		// Read-before-write
		if !cfg.DisableReadBeforeWrite {
			cur := statMtime(scope, path)
			if err := cfg.Tracker.CheckFreshness(scope, path, cur); err != nil {
				return "", err
			}
		}
		// Anti-merge: when caller asserts a type, ensure it matches existing.
		if !cfg.DisableAntiMerge {
			if mt := ParseMemoryType(asMemString(args["type"])); mt != "" {
				if cur, err := cfg.Provider.View(ctx, scope, path); err == nil {
					fm, _ := ParseMemoryFrontmatter(cur)
					if fm.Type != "" && fm.Type != mt {
						return "", fmt.Errorf("anti-merge: %s is type=%s; refusing to write %s content. Create a separate file.", path, fm.Type, mt)
					}
				}
			}
		}
		if err := cfg.Provider.StrReplace(ctx, scope, path, asMemString(args["oldStr"]), asMemString(args["newStr"])); err != nil {
			return "", err
		}
		cfg.Tracker.MarkRead(scope, path, statMtime(scope, path))
		return fmt.Sprintf("updated %s", path), nil

	case "delete":
		if !allowed[scope] {
			return "", fmt.Errorf("scope %q is not writable", scope)
		}
		if path == "" {
			return "", fmt.Errorf("'path' is required")
		}
		if !cfg.DisableReadBeforeWrite && !cfg.Tracker.HasRead(scope, path) {
			return "", fmt.Errorf("memory %s not read in this session — view it before deleting", path)
		}
		if err := cfg.Provider.Delete(ctx, scope, path); err != nil {
			return "", err
		}
		cfg.Tracker.Forget(scope, path)
		return fmt.Sprintf("deleted %s", path), nil

	case "rename":
		if !allowed[scope] {
			return "", fmt.Errorf("scope %q is not writable", scope)
		}
		oldP := asMemString(args["oldPath"])
		newP := asMemString(args["newPath"])
		if !cfg.DisableReadBeforeWrite && !cfg.Tracker.HasRead(scope, oldP) {
			return "", fmt.Errorf("memory %s not read in this session — view it before renaming", oldP)
		}
		if err := cfg.Provider.Rename(ctx, scope, oldP, newP); err != nil {
			return "", err
		}
		cfg.Tracker.Forget(scope, oldP)
		cfg.Tracker.MarkRead(scope, newP, statMtime(scope, newP))
		return fmt.Sprintf("renamed %s → %s", oldP, newP), nil
	}
	return "", fmt.Errorf("unsupported operation %q", op)
}

func joinMemoryTypes() string {
	parts := make([]string, len(MemoryTypes))
	for i, t := range MemoryTypes {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}

func parseToolScope(raw string, def Scope) Scope {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "user":
		return ScopeUser
	case "project":
		return ScopeProject
	case "*":
		return Scope("*")
	case "":
		return def
	}
	return def
}

func asMemString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case fmt.Stringer:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}
