package autobuild

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

// ── Phase 6 — Permissions v3 ────────────────────────────────────────────────
//
// Agnostic port of Claude Code's utils/permissions/ + hooks/useCanUseTool.
// Provides:
//   - Declarative `alwaysAllow` / `alwaysDeny` rules keyed by tool name + an
//     optional per-tool input matcher (e.g. bash command prefix, file glob).
//   - Permission modes mirroring Claude Code: default | acceptEdits | plan |
//     bypassPermissions | dontAsk.
//   - Pluggable approver — by default any `ask` resolves through an attached
//     InterruptGate (the same primitive used for Question/FormInput), but
//     callers can swap a custom `Approver` for headless / RPC integrations.
//
// Wire format (Claude Code compatible):
//   "Tool"             → tool-wide rule
//   "Tool(content)"    → per-input rule; parens in content are escaped \( \)
//
// The engine is opt-in. Existing `Tool.CheckPermissions` callbacks keep
// working as a fallback when no engine is registered on the dispatcher.

// PermissionBehavior is the outcome of a rule match or full Decide() call.
type PermissionBehavior string

const (
	PermissionBehaviorAllow PermissionBehavior = "allow"
	PermissionBehaviorDeny  PermissionBehavior = "deny"
	PermissionBehaviorAsk   PermissionBehavior = "ask"
)

// PermissionMode mirrors Claude Code's PermissionMode set.
type PermissionMode string

const (
	PermissionModeDefault     PermissionMode = "default"
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	PermissionModePlan        PermissionMode = "plan"
	PermissionModeBypass      PermissionMode = "bypassPermissions"
	PermissionModeDontAsk     PermissionMode = "dontAsk"
)

// PermissionRuleSource records where a rule originated. Mirrors Claude
// Code's PermissionRuleSource.
type PermissionRuleSource string

const (
	RuleSourceUserSettings    PermissionRuleSource = "userSettings"
	RuleSourceProjectSettings PermissionRuleSource = "projectSettings"
	RuleSourceLocalSettings   PermissionRuleSource = "localSettings"
	RuleSourceFlagSettings    PermissionRuleSource = "flagSettings"
	RuleSourcePolicySettings  PermissionRuleSource = "policySettings"
	RuleSourceCLI             PermissionRuleSource = "cliArg"
	RuleSourceCommand         PermissionRuleSource = "command"
	RuleSourceSession         PermissionRuleSource = "session"
)

// PermissionRuleValue selects which tool the rule applies to and, optionally,
// a per-tool input subset (RuleContent). RuleContent is matched by the tool's
// registered RuleMatcher; semantics are tool-specific.
//
//	{ToolName: "Bash", RuleContent: "npm install"} → Bash command prefix rule
//	{ToolName: "Read", RuleContent: "/src/**"}     → file path glob rule
//	{ToolName: "WebFetch", RuleContent: ""}        → tool-wide rule
type PermissionRuleValue struct {
	ToolName    string `json:"tool_name"`
	RuleContent string `json:"rule_content,omitempty"`
}

// PermissionRule pairs a value with its source and behavior.
type PermissionRule struct {
	Source       PermissionRuleSource `json:"source"`
	RuleBehavior PermissionBehavior   `json:"rule_behavior"`
	RuleValue    PermissionRuleValue  `json:"rule_value"`
}

// PermissionDecisionV3 is the Decide() outcome. UpdatedInput optionally
// rewrites the args before execution (e.g. inject defaults, sanitize paths);
// nil = pass-through.
type PermissionDecisionV3 struct {
	Behavior    PermissionBehavior   `json:"behavior"`
	Reason      string               `json:"reason,omitempty"`
	UpdatedInput map[string]any      `json:"updated_input,omitempty"`
	MatchedRule *PermissionRule     `json:"matched_rule,omitempty"`
}

// RuleMatcher decides whether a single rule applies to a specific tool call.
// Matchers are registered per tool name; if none is registered, the engine
// falls back to ExactMatcher (treats RuleContent as an exact match against
// a stringified primary argument or, when empty, a tool-wide allow).
type RuleMatcher interface {
	// Match reports whether the rule's content matches the given tool input.
	// An empty RuleContent always matches (tool-wide rule).
	Match(ruleContent string, input map[string]any) bool
}

// RuleMatcherFunc adapts a function into a RuleMatcher.
type RuleMatcherFunc func(ruleContent string, input map[string]any) bool

// Match implements RuleMatcher.
func (f RuleMatcherFunc) Match(ruleContent string, input map[string]any) bool {
	return f(ruleContent, input)
}

// ── Built-in matchers ────────────────────────────────────────────────────────

// PrefixMatcher matches when the value at `argKey` (string) starts with the
// rule's content. Used for Bash-style command rules: rule "npm install"
// matches input {"command": "npm install --no-save"}.
func PrefixMatcher(argKey string) RuleMatcher {
	return RuleMatcherFunc(func(rule string, input map[string]any) bool {
		if rule == "" {
			return true
		}
		s, _ := input[argKey].(string)
		return strings.HasPrefix(strings.TrimSpace(s), strings.TrimSpace(rule))
	})
}

// GlobMatcher matches when the value at `argKey` matches a filesystem glob
// pattern (filepath.Match semantics, with "**" treated as "match across
// path separators"). Used for file tools: rule "/src/**" matches
// {"path": "/src/lib/foo.go"}.
func GlobMatcher(argKey string) RuleMatcher {
	return RuleMatcherFunc(func(rule string, input map[string]any) bool {
		if rule == "" {
			return true
		}
		p, _ := input[argKey].(string)
		return globMatch(rule, p)
	})
}

// ExactMatcher matches when the value at `argKey` equals the rule content.
func ExactMatcher(argKey string) RuleMatcher {
	return RuleMatcherFunc(func(rule string, input map[string]any) bool {
		if rule == "" {
			return true
		}
		s, _ := input[argKey].(string)
		return s == rule
	})
}

func globMatch(pattern, target string) bool {
	if pattern == target {
		return true
	}
	// Convert ** to a regex-ish path-spanning wildcard via filepath.Match.
	// filepath.Match doesn't support **, so we expand manually: split on
	// "**" and require each chunk to be present in order.
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		idx := 0
		for i, part := range parts {
			if part == "" {
				continue
			}
			j := strings.Index(target[idx:], part)
			if j < 0 {
				return false
			}
			if i == 0 && j != 0 && !strings.HasPrefix(pattern, "**") {
				return false
			}
			idx += j + len(part)
		}
		return true
	}
	ok, _ := filepath.Match(pattern, target)
	return ok
}

// ── Wire format (Tool / Tool(content)) ───────────────────────────────────────

// ParseRuleString parses the canonical "Tool" or "Tool(content)" wire format,
// honouring escaped parens (`\(` `\)` `\\`) inside content.
func ParseRuleString(s string) PermissionRuleValue {
	open := findFirstUnescaped(s, '(')
	if open < 0 {
		return PermissionRuleValue{ToolName: s}
	}
	close := findLastUnescaped(s, ')')
	if close <= open || close != len(s)-1 {
		return PermissionRuleValue{ToolName: s}
	}
	tool := s[:open]
	if tool == "" {
		return PermissionRuleValue{ToolName: s}
	}
	raw := s[open+1 : close]
	if raw == "" || raw == "*" {
		return PermissionRuleValue{ToolName: tool}
	}
	return PermissionRuleValue{ToolName: tool, RuleContent: unescapeParens(raw)}
}

// FormatRuleString renders a PermissionRuleValue back to wire format.
func FormatRuleString(v PermissionRuleValue) string {
	if v.RuleContent == "" {
		return v.ToolName
	}
	return v.ToolName + "(" + escapeParens(v.RuleContent) + ")"
}

func escapeParens(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

func unescapeParens(s string) string {
	s = strings.ReplaceAll(s, `\(`, `(`)
	s = strings.ReplaceAll(s, `\)`, `)`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

func findFirstUnescaped(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] != c {
			continue
		}
		bs := 0
		for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
			bs++
		}
		if bs%2 == 0 {
			return i
		}
	}
	return -1
}

func findLastUnescaped(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != c {
			continue
		}
		bs := 0
		for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
			bs++
		}
		if bs%2 == 0 {
			return i
		}
	}
	return -1
}

// ── Approver (plug-in HITL) ──────────────────────────────────────────────────

// PermissionApprover bridges an `ask` decision to a human (or remote agent).
// Implementations may pop a UI dialog, post to Slack, call a webhook, etc.
//
// Returning behavior=ask collapses to deny — implementations must ultimately
// resolve to allow or deny.
type PermissionApprover interface {
	Approve(ctx context.Context, req PermissionApprovalRequest) (PermissionDecisionV3, error)
}

// PermissionApprovalRequest is the payload handed to the approver.
type PermissionApprovalRequest struct {
	ToolName string         `json:"tool_name"`
	Input    map[string]any `json:"input"`
	Reason   string         `json:"reason,omitempty"`
}

// InterruptGateApprover routes `ask` requests through an InterruptGate so the
// existing chat front-end (SSE / WebSocket) can render an approval dialog.
type InterruptGateApprover struct {
	Gate *InterruptGate
}

// Approve implements PermissionApprover.
func (a *InterruptGateApprover) Approve(ctx context.Context, req PermissionApprovalRequest) (PermissionDecisionV3, error) {
	if a == nil || a.Gate == nil {
		return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: "no approver configured"}, nil
	}
	resp, err := a.Gate.Wait(ctx, InterruptRequest{
		Kind:   InterruptKindApproval,
		Reason: req.Reason,
		Approval: &ApprovalPayload{
			ToolCall: ToolCallEntry{Name: req.ToolName},
		},
	})
	if err != nil {
		return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: err.Error()}, err
	}
	if resp.Approved {
		out := PermissionDecisionV3{Behavior: PermissionBehaviorAllow}
		if resp.ModifiedArgs != "" {
			// Best-effort: callers may parse modified args as JSON; the
			// approver only surfaces the raw payload, leaving JSON parsing
			// to the dispatcher.
			out.Reason = "approved by user"
		}
		return out, nil
	}
	return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: "denied by user"}, nil
}

// ApproverFunc adapts a function into a PermissionApprover.
type ApproverFunc func(ctx context.Context, req PermissionApprovalRequest) (PermissionDecisionV3, error)

// Approve implements PermissionApprover.
func (f ApproverFunc) Approve(ctx context.Context, req PermissionApprovalRequest) (PermissionDecisionV3, error) {
	return f(ctx, req)
}

// ── PermissionEngine ─────────────────────────────────────────────────────────

// PermissionEngine is the central decision point. Safe for concurrent use.
type PermissionEngine struct {
	mu          sync.RWMutex
	mode        PermissionMode
	allow       []PermissionRule
	deny        []PermissionRule
	matchers    map[string]RuleMatcher
	editTools   map[string]bool // tools considered "edits" for acceptEdits mode
	approver    PermissionApprover
}

// NewPermissionEngine constructs an engine in the supplied mode (default if
// empty). Matchers / rules / approver are added via the With* / Add* helpers.
func NewPermissionEngine(mode PermissionMode) *PermissionEngine {
	if mode == "" {
		mode = PermissionModeDefault
	}
	return &PermissionEngine{
		mode:      mode,
		matchers:  map[string]RuleMatcher{},
		editTools: map[string]bool{},
	}
}

// Mode returns the current mode.
func (e *PermissionEngine) Mode() PermissionMode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetMode swaps the active mode at runtime (e.g. user toggled Plan Mode).
func (e *PermissionEngine) SetMode(m PermissionMode) {
	e.mu.Lock()
	e.mode = m
	e.mu.Unlock()
}

// RegisterMatcher binds a per-tool matcher. Without a registered matcher,
// the engine falls back to a tool-wide rule check (RuleContent must be empty
// to match).
func (e *PermissionEngine) RegisterMatcher(toolName string, m RuleMatcher) {
	e.mu.Lock()
	e.matchers[toolName] = m
	e.mu.Unlock()
}

// MarkAsEditTool flags a tool as an "edit" tool so `acceptEdits` mode auto-
// approves it. Mirrors Claude Code's edit-tool fast-path.
func (e *PermissionEngine) MarkAsEditTool(toolName string) {
	e.mu.Lock()
	e.editTools[toolName] = true
	e.mu.Unlock()
}

// SetApprover swaps the human-in-the-loop bridge.
func (e *PermissionEngine) SetApprover(a PermissionApprover) {
	e.mu.Lock()
	e.approver = a
	e.mu.Unlock()
}

// AddRule appends a single rule to the alwaysAllow or alwaysDeny set
// (depending on rule.RuleBehavior).
func (e *PermissionEngine) AddRule(r PermissionRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch r.RuleBehavior {
	case PermissionBehaviorAllow:
		e.allow = append(e.allow, r)
	case PermissionBehaviorDeny:
		e.deny = append(e.deny, r)
	}
}

// AddAllowString is a convenience wrapper that parses a wire-format rule and
// stores it as an alwaysAllow rule from the given source.
func (e *PermissionEngine) AddAllowString(source PermissionRuleSource, s string) {
	e.AddRule(PermissionRule{
		Source:       source,
		RuleBehavior: PermissionBehaviorAllow,
		RuleValue:    ParseRuleString(s),
	})
}

// AddDenyString is the deny counterpart of AddAllowString.
func (e *PermissionEngine) AddDenyString(source PermissionRuleSource, s string) {
	e.AddRule(PermissionRule{
		Source:       source,
		RuleBehavior: PermissionBehaviorDeny,
		RuleValue:    ParseRuleString(s),
	})
}

// Rules returns a snapshot of (allow, deny) rule sets.
func (e *PermissionEngine) Rules() (allow, deny []PermissionRule) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	allow = append(allow, e.allow...)
	deny = append(deny, e.deny...)
	return
}

// Decide runs the canonical algorithm:
//  1. bypassPermissions  → allow.
//  2. plan + !readOnly   → ask (or deny if dontAsk).
//  3. alwaysDeny match   → deny.
//  4. acceptEdits + edit → allow.
//  5. alwaysAllow match  → allow.
//  6. tool.CheckPermissions (if set) → may short-circuit allow/deny.
//  7. otherwise          → ask.
//  8. dontAsk converts ask → deny at the very end.
//
// When the final behavior is `ask` and the engine has an approver wired,
// the engine awaits the approver and returns its (allow|deny) verdict.
func (e *PermissionEngine) Decide(ctx context.Context, tool *Tool, input map[string]any) PermissionDecisionV3 {
	if tool == nil {
		return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: "tool not found"}
	}
	if input == nil {
		input = map[string]any{}
	}

	e.mu.RLock()
	mode := e.mode
	allow := append([]PermissionRule(nil), e.allow...)
	deny := append([]PermissionRule(nil), e.deny...)
	matcher := e.matchers[tool.Name]
	isEdit := e.editTools[tool.Name]
	approver := e.approver
	e.mu.RUnlock()

	if matcher == nil {
		matcher = ruleWideOnlyMatcher{}
	}

	readOnly := tool.IsReadOnly != nil && tool.IsReadOnly(input)

	// 1. Bypass
	if mode == PermissionModeBypass {
		return PermissionDecisionV3{Behavior: PermissionBehaviorAllow, Reason: "bypassPermissions mode"}
	}

	// 2. Plan mode
	if mode == PermissionModePlan && !readOnly {
		return e.finalize(ctx, tool, input, PermissionDecisionV3{
			Behavior: PermissionBehaviorAsk,
			Reason:   "plan mode: only read-only tools auto-approve",
		}, approver, mode)
	}

	// 3. Always-deny rules
	for i := range deny {
		r := deny[i]
		if r.RuleValue.ToolName != tool.Name {
			continue
		}
		if matcher.Match(r.RuleValue.RuleContent, input) {
			return PermissionDecisionV3{
				Behavior:    PermissionBehaviorDeny,
				Reason:      "denied by rule " + FormatRuleString(r.RuleValue),
				MatchedRule: &r,
			}
		}
	}

	// 4. Accept-edits fast path
	if mode == PermissionModeAcceptEdits && isEdit {
		return PermissionDecisionV3{Behavior: PermissionBehaviorAllow, Reason: "acceptEdits mode"}
	}

	// 5. Always-allow rules
	for i := range allow {
		r := allow[i]
		if r.RuleValue.ToolName != tool.Name {
			continue
		}
		if matcher.Match(r.RuleValue.RuleContent, input) {
			return PermissionDecisionV3{
				Behavior:    PermissionBehaviorAllow,
				Reason:      "allowed by rule " + FormatRuleString(r.RuleValue),
				MatchedRule: &r,
			}
		}
	}

	// 6. Tool-level CheckPermissions fallback
	if tool.CheckPermissions != nil {
		res, err := tool.CheckPermissions(ctx, input)
		if err != nil {
			return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: err.Error()}
		}
		switch res.Decision {
		case PermissionAllow:
			return PermissionDecisionV3{Behavior: PermissionBehaviorAllow, Reason: res.Reason, UpdatedInput: res.UpdatedArgs}
		case PermissionDeny:
			return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: res.Reason}
		case PermissionAskUser:
			return e.finalize(ctx, tool, input, PermissionDecisionV3{Behavior: PermissionBehaviorAsk, Reason: res.Reason}, approver, mode)
		}
	}

	// 7. Default → ask
	return e.finalize(ctx, tool, input, PermissionDecisionV3{Behavior: PermissionBehaviorAsk}, approver, mode)
}

// finalize applies dontAsk + approver routing for `ask` decisions.
func (e *PermissionEngine) finalize(ctx context.Context, tool *Tool, input map[string]any, d PermissionDecisionV3, approver PermissionApprover, mode PermissionMode) PermissionDecisionV3 {
	if d.Behavior != PermissionBehaviorAsk {
		return d
	}
	if mode == PermissionModeDontAsk {
		return PermissionDecisionV3{
			Behavior: PermissionBehaviorDeny,
			Reason:   "dontAsk mode: " + d.Reason,
		}
	}
	if approver == nil {
		return PermissionDecisionV3{
			Behavior: PermissionBehaviorDeny,
			Reason:   "no approver configured for ask decision",
		}
	}
	res, err := approver.Approve(ctx, PermissionApprovalRequest{
		ToolName: tool.Name,
		Input:    input,
		Reason:   d.Reason,
	})
	if err != nil {
		return PermissionDecisionV3{Behavior: PermissionBehaviorDeny, Reason: err.Error()}
	}
	if res.Behavior == "" {
		res.Behavior = PermissionBehaviorDeny
	}
	return res
}

// ruleWideOnlyMatcher matches only when RuleContent is empty (tool-wide).
type ruleWideOnlyMatcher struct{}

// Match implements RuleMatcher.
func (ruleWideOnlyMatcher) Match(ruleContent string, _ map[string]any) bool {
	return ruleContent == ""
}

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrPermissionDenied is returned when the engine denies a tool call. The
// dispatcher converts this into a tool error message visible to the LLM.
var ErrPermissionDenied = errors.New("permission denied")
