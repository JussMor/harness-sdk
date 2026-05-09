package autobuild

import "strings"

// ─── Memory prompt guidance ────────────────────────────────────────────────
//
// Behavioural instructions injected as a system-prompt section so the LLM
// knows the canonical 4-type taxonomy, what NOT to save, and the two-step
// write protocol (write file → add line to MEMORY.md index).
//
// Mirrors Claude Code's memdir prompt structure (memdir.ts:buildMemoryLines)
// but trimmed to the parts that pay off in eval: type taxonomy, exclusions,
// two-step write, freshness/recall guidance.

// MemoryGuidanceOptions controls which sections appear in BuildMemoryGuidance.
type MemoryGuidanceOptions struct {
	// MemoryDir is the on-disk path the model should write to. Surfaced
	// in the prompt so the LLM doesn't waste turns running ls/mkdir.
	MemoryDir string
	// SkipIndex disables the two-step write protocol (no MEMORY.md).
	// Use only when the deployment uses single-file flat memory.
	SkipIndex bool
	// ExtraGuidelines is appended verbatim at the bottom — for project
	// or deployment-specific rules.
	ExtraGuidelines []string
}

// BuildMemoryGuidance returns the markdown block to inject into LayerMemory
// (or LayerCore) so the agent treats the memory system correctly.
//
// Output structure:
//   - "# Memory" header + path + dir-exists guidance
//   - Types-of-memory taxonomy with examples
//   - What NOT to save
//   - How to save (two-step protocol with frontmatter example)
//   - When to access memories
//   - Freshness/staleness caveat
func BuildMemoryGuidance(opts MemoryGuidanceOptions) string {
	dir := opts.MemoryDir
	if dir == "" {
		dir = "memory/"
	}

	var b strings.Builder
	b.WriteString("# Memory\n\n")
	b.WriteString("You have a persistent, file-based memory system at `")
	b.WriteString(dir)
	b.WriteString("`. This directory already exists — write to it directly (do not run mkdir or check for its existence).\n\n")
	b.WriteString("Build up this memory over time so future conversations have a complete picture of who the user is, how they want to collaborate, what to avoid or repeat, and the context behind their work.\n\n")
	b.WriteString("If the user explicitly asks you to remember something, save it as whichever type fits best. If they ask you to forget something, find and remove the relevant entry.\n\n")

	b.WriteString(toolCallContractSection)
	b.WriteString(memoryTypesSection)
	b.WriteString(whatNotToSaveSection)

	if opts.SkipIndex {
		b.WriteString(howToSaveSectionFlat)
	} else {
		b.WriteString(howToSaveSectionTwoStep)
	}

	b.WriteString(whenToAccessSection)
	b.WriteString(trustingRecallSection)

	if len(opts.ExtraGuidelines) > 0 {
		b.WriteString("\n## Project-specific guidelines\n\n")
		for _, g := range opts.ExtraGuidelines {
			b.WriteString("- ")
			b.WriteString(g)
			b.WriteString("\n")
		}
	}
	return b.String()
}

const toolCallContractSection = "## Saving and retrieving REQUIRE tool calls\n\n" +
	"The memory system is a set of files on disk. The ONLY way to read, write, update, or delete a memory is by invoking the corresponding tool: `memory_view`, `memory_create`, `memory_str_replace`, `memory_delete`, `memory_list`.\n\n" +
	"Saying \"guardado\", \"saved\", \"noted\", \"I'll remember that\", \"recordé\", \"borré\", \"olvidé\", \"actualizado\" without calling a tool **does NOT save anything**. The user sees only an empty memory directory and loses trust. If you intend to remember/forget something, you MUST call the corresponding tool in the SAME turn — never as a \"next step\".\n\n" +
	"Hard rules:\n" +
	"- Before claiming a memory was saved, the assistant message MUST be in the same turn as a successful `memory_create` or `memory_str_replace` tool call.\n" +
	"- Before claiming a memory was deleted/forgotten, you MUST have called `memory_delete` in this turn. If the file doesn't exist, say \"no encontré nada sobre X en memoria\" — do NOT say \"borré\" / \"olvidé\".\n" +
	"- Recalling a specific memory ALSO requires a tool call (`memory_view`). Do not paraphrase what you \"remember\" without reading the file first.\n" +
	"- If the user reports \"no se reflejó\" / \"no veo nada en la UI\", that means you skipped the tool call. Do it now — do not argue.\n\n" +
	"### Scopes are independent — each has its own MEMORY.md\n\n" +
	"There are two scopes: `user` (cross-project: things true about the user across ALL their work — role, personal prefs, working style) and `project` (this conversation/codebase: sprints, deadlines, stakeholders, dashboards specific to this work). EACH scope has its OWN `MEMORY.md` index file. The read-before-write contract is per-scope: viewing `user/MEMORY.md` does NOT unlock writes to `project/`. Before your first `memory_create` in a scope this turn, call `memory_view` on that scope's `/MEMORY.md`.\n\n" +
	"**Choosing scope:** ask yourself \"would this be true if the user opened a totally different project tomorrow?\" If yes → `user`. If no (it's about this codebase, sprint, dashboard, team) → `project`. The TYPE (user/feedback/project/reference) is independent of the SCOPE — you can have a `type: reference` memory in either scope.\n\n" +
	"### When to save proactively (no explicit \"remember\" needed)\n\n" +
	"Save WITHOUT being asked when the user reveals any of these — it is part of building memory over time:\n" +
	"- A URL, dashboard, or external system pointer (\"los logs viven en Grafana: https://...\") → `type: reference`. Scope by context: a personal tool the user always uses → `user`; a dashboard specific to this project's prod → `project`.\n" +
	"- A rule about how to work (\"no agregues docstrings\", \"siempre usa X\") → `type: feedback`. Scope by context: a personal style rule → `user`; a project-specific convention → `project`.\n" +
	"- A deadline, sprint, stakeholder, or ongoing initiative (\"el sprint termina el jueves\") → `scope: project`, `type: project`.\n" +
	"- Their role, expertise, or stack (\"soy backend Go\") → `scope: user`, `type: user`.\n\n" +
	"### \"Olvida X\" / \"forget X\" protocol\n\n" +
	"1. Call `memory_list` (or scan the always-loaded MEMORY.md) to find files matching X.\n" +
	"2. If found → call `memory_delete` on each. Then say \"borré N entrada(s) sobre X\".\n" +
	"3. If NOT found → respond \"no encontré nada sobre X en memoria\". Do NOT say \"borré\" / \"olvidé\" — the user will catch the lie.\n\n" +
	"### One memory per type — do NOT merge across types\n\n" +
	"Each of the 4 types lives in its own file(s). Do NOT add a `feedback` rule (\"sin docstrings\") into a `user` profile file just because the topic feels related. Different type → different file. The dup-check is per-description Jaccard within scope; if it passes, create the new file.\n\n"

const memoryTypesSection = `## Types of memory

Memories are constrained to four types. Content derivable from the project state (code patterns, architecture, git history, file structure) is NOT a memory — it can be re-derived with grep/git.

<types>
<type>
  <name>user</name>
  <description>The user's role, goals, responsibilities, expertise. Lets you tailor explanations to their mental model (e.g. frame frontend explanations in terms of backend analogues for a Go-deep, React-new user).</description>
  <when_to_save>When you learn details about the user's role, preferences, responsibilities, or knowledge.</when_to_save>
</type>
<type>
  <name>feedback</name>
  <description>Guidance the user gave you about how to approach work — both what to AVOID and what to KEEP doing. Save corrections AND validated approaches: corrections-only memory drifts toward over-cautiousness.</description>
  <when_to_save>Whenever the user corrects your approach ("no, not that", "stop doing X") OR confirms a non-obvious approach worked ("perfect, keep doing that"). Watch for the quieter confirmations.</when_to_save>
  <body_structure>Lead with the rule, then **Why:** (the reason — usually a past incident or strong preference) and **How to apply:** (when this kicks in). The why lets you judge edge cases.</body_structure>
</type>
<type>
  <name>project</name>
  <description>Ongoing work, deadlines, incidents, motivations behind initiatives — context NOT in code or git history.</description>
  <when_to_save>When you learn who is doing what, why, or by when. These decay fast — convert relative dates ("Thursday") to absolute ("2026-05-15") on save so the memory stays interpretable later.</when_to_save>
  <body_structure>Lead with the fact, then **Why:** (constraint, deadline, stakeholder ask) and **How to apply:** (how this should shape your suggestions).</body_structure>
</type>
<type>
  <name>reference</name>
  <description>Pointers to external systems where up-to-date info lives (Linear projects, Grafana dashboards, Slack channels). The memory says WHERE to look, not WHAT is there.</description>
  <when_to_save>When you learn about an external resource and its purpose.</when_to_save>
</type>
</types>

`

const whatNotToSaveSection = `## What NOT to save

- Code patterns, conventions, architecture, file paths — derivable from current code. **Example to refuse: "save that our HTTP handlers return {data, error, meta}" — the response shape lives in the code.**
- Git history, recent changes, who-changed-what — git log/git blame is authoritative.
- Debugging fixes — the fix is in the code; the commit message has the context.
- Anything already in CLAUDE.md / AGENTS.md.
- Ephemeral task state — current conversation context, in-progress work.

These exclusions apply EVEN when the user asks you to save. If they say "save this PR list", ask what was *surprising* or *non-obvious* about it — that is the part worth keeping.

`

const howToSaveSectionTwoStep = `## How to save memories

Saving is a two-step process:

**Step 1** — write the memory to its own file (e.g. ` + "`user_role.md`" + `, ` + "`feedback_testing.md`" + `) with this frontmatter:

` + "```" + `markdown
---
name: short-slug
description: One-line summary of what this memory captures.
type: user|feedback|project|reference
---

Memory body here…
` + "```" + `

**Step 2** — add a one-line pointer to ` + "`" + EntrypointName + "`" + `:

` + "```" + `markdown
- [Title](file.md) — one-line hook
` + "```" + `

` + "`" + EntrypointName + "`" + ` is an INDEX, not a memory. It is always loaded into your context — keep entries short. Lines past the cap are truncated.

- Keep name/description/type fields up-to-date with the body.
- Organize semantically by topic, not chronologically.
- Update or remove memories that turn out to be wrong or stale.
- **Before creating a new file, ALWAYS read ` + "`MEMORY.md`" + ` first** and check if an existing file already covers the topic. If yes, use ` + "`memory_view`" + ` to read it, then ` + "`memory_str_replace`" + ` to update — do NOT create a parallel file with a different name.

`

const howToSaveSectionFlat = `## How to save memories

Write each memory to its own file (e.g. ` + "`user_role.md`" + `, ` + "`feedback_testing.md`" + `) with this frontmatter:

` + "```" + `markdown
---
name: short-slug
description: One-line summary.
type: user|feedback|project|reference
---

Memory body here…
` + "```" + `

- Keep name/description/type up-to-date with the body.
- Organize semantically by topic, not chronologically.
- Update or remove memories that turn out to be wrong or stale.
- **Before creating a new file, list memory first** and check if an existing file already covers the topic. Update it instead of creating a parallel file with a different name.

`

const whenToAccessSection = `## When to access memories

Read relevant memories when the user's request touches an area where prior context could help:
- Their preferences, expertise, or working style → check user memories.
- "How should I do X here?" → check feedback memories before defaulting to generic advice.
- "What's the status of Y?" / "Why are we doing Z?" → check project memories.
- "Where do I find…?" → check reference memories.

Do NOT load the full memory directory by default. Surface only what is relevant to the current turn.

`

const trustingRecallSection = `## Trusting memory recall

Memories are point-in-time observations, not live state. Treat them as informed priors, not facts:
- A 47-day-old memory that cites ` + "`pkg/foo.go:120`" + ` is a starting point — verify the citation in the current code before asserting it as fact.
- If a memory contradicts the current project state, prefer the current state and update or remove the memory.
- A stale citation can sound MORE authoritative than fresh recall — be skeptical of confident-but-old memories.

## System reminders

Tool results may contain ` + "`<system-reminder>`" + ` tags. These are added by the system, not by the user. They contain authoritative information about side effects (e.g. "the index was updated automatically", "duplicate detected") and constraints. Read them carefully and follow them — they bear no direct relation to the user's literal request and should not be repeated back to the user verbatim.

`
