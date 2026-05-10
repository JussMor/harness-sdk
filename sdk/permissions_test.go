package autobuild

import (
	"context"
	"testing"
)

func TestParseRuleString(t *testing.T) {
	cases := []struct {
		in   string
		tool string
		body string
	}{
		{"Bash", "Bash", ""},
		{"Bash()", "Bash", ""},
		{"Bash(*)", "Bash", ""},
		{"Bash(npm install)", "Bash", "npm install"},
		{`Bash(python -c "print\(1\)")`, "Bash", `python -c "print(1)"`},
		{"(foo)", "(foo)", ""}, // malformed: empty toolName
	}
	for _, c := range cases {
		v := ParseRuleString(c.in)
		if v.ToolName != c.tool || v.RuleContent != c.body {
			t.Errorf("ParseRuleString(%q) = {%q,%q}, want {%q,%q}", c.in, v.ToolName, v.RuleContent, c.tool, c.body)
		}
	}
}

func TestFormatRuleStringRoundTrip(t *testing.T) {
	v := PermissionRuleValue{ToolName: "Bash", RuleContent: `python -c "print(1)"`}
	s := FormatRuleString(v)
	v2 := ParseRuleString(s)
	if v2 != v {
		t.Errorf("round-trip lost data: %q → %#v", s, v2)
	}
}

func TestPrefixMatcher(t *testing.T) {
	m := PrefixMatcher("command")
	if !m.Match("npm install", map[string]any{"command": "npm install --no-save"}) {
		t.Error("expected prefix match")
	}
	if m.Match("npm install", map[string]any{"command": "yarn install"}) {
		t.Error("unexpected match")
	}
	if !m.Match("", map[string]any{}) {
		t.Error("empty rule must match (tool-wide)")
	}
}

func TestGlobMatcher(t *testing.T) {
	m := GlobMatcher("path")
	if !m.Match("/src/**", map[string]any{"path": "/src/lib/foo.go"}) {
		t.Error("expected ** glob match")
	}
	if m.Match("/src/**", map[string]any{"path": "/etc/hosts"}) {
		t.Error("unexpected match")
	}
}

func TestPermissionEngineDecide_BypassMode(t *testing.T) {
	e := NewPermissionEngine(PermissionModeBypass)
	tool := &Tool{Name: "Bash"}
	d := e.Decide(context.Background(), tool, map[string]any{"command": "rm -rf /"})
	if d.Behavior != PermissionBehaviorAllow {
		t.Fatalf("bypass should allow; got %s", d.Behavior)
	}
}

func TestPermissionEngineDecide_DenyOverridesAllow(t *testing.T) {
	e := NewPermissionEngine(PermissionModeDefault)
	e.RegisterMatcher("Bash", PrefixMatcher("command"))
	e.AddAllowString(RuleSourceUserSettings, "Bash")
	e.AddDenyString(RuleSourceUserSettings, "Bash(rm)")

	tool := &Tool{Name: "Bash"}
	d := e.Decide(context.Background(), tool, map[string]any{"command": "rm -rf foo"})
	if d.Behavior != PermissionBehaviorDeny {
		t.Fatalf("deny rule should win; got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestPermissionEngineDecide_AllowMatchesPrefix(t *testing.T) {
	e := NewPermissionEngine(PermissionModeDefault)
	e.RegisterMatcher("Bash", PrefixMatcher("command"))
	e.AddAllowString(RuleSourceUserSettings, "Bash(npm)")

	tool := &Tool{Name: "Bash"}
	d := e.Decide(context.Background(), tool, map[string]any{"command": "npm install"})
	if d.Behavior != PermissionBehaviorAllow {
		t.Fatalf("expected allow; got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestPermissionEngineDecide_AskWithoutApprover(t *testing.T) {
	e := NewPermissionEngine(PermissionModeDefault)
	tool := &Tool{Name: "Bash"}
	d := e.Decide(context.Background(), tool, nil)
	if d.Behavior != PermissionBehaviorDeny {
		t.Fatalf("ask with no approver must collapse to deny; got %s", d.Behavior)
	}
}

func TestPermissionEngineDecide_AskRoutesToApprover(t *testing.T) {
	e := NewPermissionEngine(PermissionModeDefault)
	e.SetApprover(ApproverFunc(func(_ context.Context, req PermissionApprovalRequest) (PermissionDecisionV3, error) {
		if req.ToolName != "Bash" {
			t.Errorf("approver got tool %q, want Bash", req.ToolName)
		}
		return PermissionDecisionV3{Behavior: PermissionBehaviorAllow, Reason: "approved"}, nil
	}))
	tool := &Tool{Name: "Bash"}
	d := e.Decide(context.Background(), tool, map[string]any{"command": "ls"})
	if d.Behavior != PermissionBehaviorAllow || d.Reason != "approved" {
		t.Fatalf("expected approved allow; got %+v", d)
	}
}

func TestPermissionEngineDecide_DontAskCollapsesAskToDeny(t *testing.T) {
	e := NewPermissionEngine(PermissionModeDontAsk)
	// Even with an approver, dontAsk denies.
	e.SetApprover(ApproverFunc(func(_ context.Context, _ PermissionApprovalRequest) (PermissionDecisionV3, error) {
		t.Fatal("approver must not be called in dontAsk mode")
		return PermissionDecisionV3{}, nil
	}))
	tool := &Tool{Name: "Bash"}
	d := e.Decide(context.Background(), tool, nil)
	if d.Behavior != PermissionBehaviorDeny {
		t.Fatalf("expected deny; got %s", d.Behavior)
	}
}

func TestPermissionEngineDecide_PlanModeAllowsReadOnly(t *testing.T) {
	e := NewPermissionEngine(PermissionModePlan)
	tool := &Tool{Name: "Read", IsReadOnly: func(_ map[string]any) bool { return true }}
	e.AddAllowString(RuleSourceUserSettings, "Read") // tool-wide allow
	d := e.Decide(context.Background(), tool, map[string]any{"path": "/etc/hosts"})
	if d.Behavior != PermissionBehaviorAllow {
		t.Fatalf("plan mode + read-only should allow via rule; got %s (%s)", d.Behavior, d.Reason)
	}
}

func TestPermissionEngineDecide_PlanModeBlocksMutation(t *testing.T) {
	e := NewPermissionEngine(PermissionModePlan)
	tool := &Tool{Name: "Write"} // not read-only
	d := e.Decide(context.Background(), tool, nil)
	if d.Behavior != PermissionBehaviorDeny {
		t.Fatalf("plan + mutation + no approver should deny; got %s", d.Behavior)
	}
}

func TestPermissionEngineDecide_AcceptEditsForEditTools(t *testing.T) {
	e := NewPermissionEngine(PermissionModeAcceptEdits)
	e.MarkAsEditTool("Edit")
	tool := &Tool{Name: "Edit"}
	d := e.Decide(context.Background(), tool, nil)
	if d.Behavior != PermissionBehaviorAllow {
		t.Fatalf("acceptEdits should allow edit tools; got %s", d.Behavior)
	}
}

func TestPermissionEngineDecide_ToolCheckPermissionsFallback(t *testing.T) {
	e := NewPermissionEngine(PermissionModeDefault)
	tool := &Tool{
		Name: "Custom",
		CheckPermissions: func(_ context.Context, _ map[string]any) (PermissionResult, error) {
			return PermissionResult{Decision: PermissionAllow, Reason: "tool said yes"}, nil
		},
	}
	d := e.Decide(context.Background(), tool, nil)
	if d.Behavior != PermissionBehaviorAllow || d.Reason != "tool said yes" {
		t.Fatalf("expected tool fallback to allow; got %+v", d)
	}
}

func TestDispatcherUsesPermissionEngine(t *testing.T) {
	reg := NewToolRegistry()
	called := false
	reg.Register(&Tool{
		Name: "Bash",
		Execute: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			called = true
			return "ok", nil
		},
	})
	eng := NewPermissionEngine(PermissionModeDefault)
	eng.RegisterMatcher("Bash", PrefixMatcher("command"))
	eng.AddDenyString(RuleSourceUserSettings, "Bash(rm)")

	d := NewToolDispatcher(reg, nil).WithPermissions(eng)
	res := d.Dispatch(context.Background(), ToolCallEntry{
		ID:        "1",
		Name:      "Bash",
		Arguments: `{"command":"rm -rf /"}`,
	}, "")
	if called {
		t.Error("Execute must not run when denied")
	}
	if res.Error == nil {
		t.Error("expected dispatch to surface deny as error")
	}
}
