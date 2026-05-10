package autobuild

import (
	"strings"
)

// SystemReminderTag is the canonical XML-ish wrapper used to inject
// out-of-band guidance into the conversation. Mirrors the convention used by
// Claude Code (see /Users/jussmor/Developer/Claude Code/constants/prompts.ts).
//
// Anything wrapped in <system-reminder>...</system-reminder> is treated by the
// model as a non-user, non-assistant directive: it must be obeyed but not
// echoed back, never quoted, and never mentioned to the user.
const SystemReminderTag = "system-reminder"

// SystemReminder wraps body text in <system-reminder> tags. Empty or
// whitespace-only bodies return "" so callers can safely concatenate.
//
// The body is NOT trimmed of internal whitespace — callers control formatting.
func SystemReminder(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(SystemReminderTag)
	b.WriteString(">\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</")
	b.WriteString(SystemReminderTag)
	b.WriteString(">")
	return b.String()
}

// JoinSystemReminders concatenates multiple reminder blocks with a blank line
// between them. Empty entries are skipped. Returns "" when nothing remains.
//
// Use this to collect dynamic listings (skills, agents, memory) into a single
// attachment payload that can be appended to a message or system prompt.
func JoinSystemReminders(blocks ...string) string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		b = strings.TrimRight(b, "\n")
		if strings.TrimSpace(b) == "" {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n\n")
}
