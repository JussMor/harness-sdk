package autobuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════
// Frontmatter parser (zero dependencies — stdlib only)
// ═══════════════════════════════════════════════════════════════════════

// parseFrontmatter splits a markdown file into YAML frontmatter key-value
// pairs and the body content. Frontmatter must be delimited by "---" lines.
// Returns (fields map, list-fields map, body, error).
func parseFrontmatter(raw string) (map[string]string, map[string][]string, string, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil, nil, raw, fmt.Errorf("no frontmatter delimiter found")
	}

	// Find the closing ---
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return nil, nil, raw, fmt.Errorf("unclosed frontmatter — missing closing ---")
	}

	fields := make(map[string]string)
	lists := make(map[string][]string)

	var currentListKey string
	for _, line := range lines[1:endIdx] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// List item: "  - value"
		if strings.HasPrefix(trimmed, "- ") && currentListKey != "" {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			lists[currentListKey] = append(lists[currentListKey], val)
			continue
		}

		// Key: value pair
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])

		if val == "" {
			// This key introduces a list
			currentListKey = key
		} else {
			currentListKey = ""
			fields[key] = val
		}
	}

	body := strings.Join(lines[endIdx+1:], "\n")
	body = strings.TrimLeft(body, "\n")

	return fields, lists, body, nil
}

// ═══════════════════════════════════════════════════════════════════════
// ParseModeFile — parse a system.md into a Mode
// ═══════════════════════════════════════════════════════════════════════

// ParseModeFile reads a system.md file and returns a fully populated Mode.
// The file MUST contain YAML frontmatter with at least: id, name, base_mode.
// Everything after the frontmatter becomes the Mode.PromptContent.
func ParseModeFile(path string) (*Mode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mode file %s: %w", path, err)
	}
	return ParseModeMarkdown(string(data))
}

// ParseModeMarkdown parses a system.md string (frontmatter + body) into a Mode.
func ParseModeMarkdown(raw string) (*Mode, error) {
	fields, lists, body, err := parseFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("parse mode frontmatter: %w", err)
	}

	// Validate required fields
	for _, req := range []string{"id", "name", "base_mode"} {
		if fields[req] == "" {
			return nil, fmt.Errorf("missing required mode field: %s", req)
		}
	}

	meta := ModeMeta{
		ID:              fields["id"],
		Name:            fields["name"],
		BaseMode:        fields["base_mode"],
		ToolsMode:       fields["tools_mode"],
		Tools:           lists["tools"],
		Model:           fields["model"],
		ReasoningEffort: fields["reasoning_effort"],
		Temperature:     fields["temperature"],
		Author:          fields["author"],
		Created:         fields["created"],
	}

	mode := &Mode{
		Meta:          meta,
		ID:            meta.ID,
		Name:          meta.Name,
		BaseModeID:    BaseMode(meta.BaseMode),
		PromptContent: body,
	}

	switch meta.ToolsMode {
	case "allowlist":
		mode.ToolsMode = ToolsModeAllowlist
	case "denylist":
		mode.ToolsMode = ToolsModeDenylist
	}

	mode.ToolsList = meta.Tools

	// Model settings (only if any field is set)
	if meta.Model != "" || meta.ReasoningEffort != "" || meta.Temperature != "" {
		ms := &ModelSettings{
			Model:           meta.Model,
			ReasoningEffort: meta.ReasoningEffort,
		}
		if meta.Temperature != "" {
			if t, err := strconv.ParseFloat(meta.Temperature, 64); err == nil {
				ms.Temperature = t
			}
		}
		mode.ModelSettings = ms
	}

	return mode, nil
}

// LoadModesDir walks a directory and parses every system.md found one level
// deep (i.e. prompts/<mode-name>/system.md). Returns all successfully parsed modes.
func LoadModesDir(dir string) ([]*Mode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read modes dir %s: %w", dir, err)
	}

	var modes []*Mode
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		modePath := filepath.Join(dir, entry.Name(), "system.md")
		m, err := ParseModeFile(modePath)
		if err != nil {
			continue // skip dirs without a valid system.md
		}
		modes = append(modes, m)
	}
	return modes, nil
}

// LoadModeProviderFromDirs loads the first directory that contains at least one
// valid mode and returns an in-memory ModeProvider for those modes.
func LoadModeProviderFromDirs(dirs ...string) (ModeProvider, error) {
	for _, dir := range dirs {
		modes, err := LoadModesDir(dir)
		if err != nil || len(modes) == 0 {
			continue
		}
		return NewStaticModeProvider(modes), nil
	}

	return nil, fmt.Errorf("no SDK modes directory available")
}
