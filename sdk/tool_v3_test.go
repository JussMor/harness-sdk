package autobuild

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestToolDefaultsAreSafeWhenPredicatesNil(t *testing.T) {
	tool := &Tool{Name: "noop"}
	if tool.ReadOnly(nil) {
		t.Errorf("nil IsReadOnly must default to false (mutating)")
	}
	if tool.ConcurrencySafe(nil) {
		t.Errorf("nil IsConcurrencySafe must default to false (serial)")
	}
	if tool.Destructive(nil) {
		t.Errorf("nil IsDestructive must default to false")
	}
}

func TestRegistryAliasLookup(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&Tool{Name: "memory_view", Aliases: []string{"view_memory", "mem_view"}})

	if r.Get("memory_view") == nil {
		t.Fatal("canonical name must resolve")
	}
	if r.Get("view_memory") == nil {
		t.Fatal("alias must resolve")
	}
	if r.Get("mem_view") == nil {
		t.Fatal("second alias must resolve")
	}
	if r.Get("missing") != nil {
		t.Fatal("unknown name must return nil")
	}

	// Aliases must NOT inflate List/Names/ToolDefs.
	if got := len(r.List()); got != 1 {
		t.Errorf("List() = %d entries, want 1 (aliases must not double-count)", got)
	}
	if got := len(r.ToolDefs()); got != 1 {
		t.Errorf("ToolDefs() = %d entries, want 1", got)
	}
}

func TestToolDefsExcludeHiddenAndDeferred(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&Tool{Name: "visible"})
	r.Register(&Tool{Name: "hidden_one", Hidden: true})
	r.Register(&Tool{Name: "deferred_one", Deferred: true})

	defs := r.ToolDefs()
	if len(defs) != 1 {
		t.Fatalf("ToolDefs() = %d, want 1 (only visible)", len(defs))
	}
	if defs[0].Function.Name != "visible" {
		t.Errorf("ToolDefs() returned %q, want visible", defs[0].Function.Name)
	}
}

func TestDispatchRunsValidateBeforeExecute(t *testing.T) {
	executed := false
	r := NewToolRegistry()
	r.Register(&Tool{
		Name: "guarded",
		Validate: func(_ context.Context, args map[string]any) error {
			if _, ok := args["path"]; !ok {
				return errors.New("path is required")
			}
			return nil
		},
		Execute: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			executed = true
			return "ok", nil
		},
	})

	d := NewToolDispatcher(r, nil)
	res := d.Dispatch(context.Background(), ToolCallEntry{ID: "1", Name: "guarded", Arguments: "{}"}, "")

	if executed {
		t.Fatal("Execute must not run when Validate fails")
	}
	if res.Error == nil {
		t.Fatal("dispatch must propagate validation error")
	}
	if !strings.Contains(res.Content, "path is required") {
		t.Errorf("result content %q must include validation reason", res.Content)
	}
}

func TestDispatchPermissionDenyShortCircuits(t *testing.T) {
	executed := false
	r := NewToolRegistry()
	r.Register(&Tool{
		Name: "guarded",
		CheckPermissions: func(_ context.Context, _ map[string]any) (PermissionResult, error) {
			return PermissionResult{Decision: PermissionDeny, Reason: "blocked by policy"}, nil
		},
		Execute: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			executed = true
			return "ok", nil
		},
	})

	d := NewToolDispatcher(r, nil)
	res := d.Dispatch(context.Background(), ToolCallEntry{ID: "1", Name: "guarded"}, "")

	if executed {
		t.Fatal("Execute must not run on PermissionDeny")
	}
	if res.Error == nil || !strings.Contains(res.Content, "blocked by policy") {
		t.Errorf("denial reason missing from content: %q", res.Content)
	}
}

func TestDispatchPermissionUpdatedArgsAreApplied(t *testing.T) {
	var seen map[string]any
	r := NewToolRegistry()
	r.Register(&Tool{
		Name: "rewriter",
		CheckPermissions: func(_ context.Context, args map[string]any) (PermissionResult, error) {
			args["injected"] = true
			return PermissionResult{Decision: PermissionAllow, UpdatedArgs: args}, nil
		},
		Execute: func(_ context.Context, _ string, args map[string]any) (string, error) {
			seen = args
			return "ok", nil
		},
	})

	d := NewToolDispatcher(r, nil)
	_ = d.Dispatch(context.Background(), ToolCallEntry{ID: "1", Name: "rewriter", Arguments: `{"x":1}`}, "")
	if seen["injected"] != true {
		t.Errorf("execute did not see permission-injected arg, got %v", seen)
	}
}

func TestSystemReminderWrapping(t *testing.T) {
	if got := SystemReminder("   "); got != "" {
		t.Errorf("blank body must yield empty string, got %q", got)
	}
	got := SystemReminder("hello")
	want := "<system-reminder>\nhello\n</system-reminder>"
	if got != want {
		t.Errorf("SystemReminder(hello) = %q, want %q", got, want)
	}
}

func TestJoinSystemRemindersSkipsEmpty(t *testing.T) {
	got := JoinSystemReminders("", "a", "   ", "b")
	want := "a\n\nb"
	if got != want {
		t.Errorf("JoinSystemReminders = %q, want %q", got, want)
	}
}

func TestCollectDynamicRemindersStableOrder(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&Tool{Name: "z_tool", DynamicReminder: func(_ context.Context) (string, error) { return "Z block", nil }})
	r.Register(&Tool{Name: "a_tool", DynamicReminder: func(_ context.Context) (string, error) { return "A block", nil }})
	r.Register(&Tool{Name: "m_tool", DynamicReminder: func(_ context.Context) (string, error) { return "", nil }})

	blocks, err := r.CollectDynamicReminders(context.Background())
	if err != nil {
		t.Fatalf("CollectDynamicReminders returned error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (empty dropped)", len(blocks))
	}
	if blocks[0] != "A block" || blocks[1] != "Z block" {
		t.Errorf("blocks not in alphabetical order: %v", blocks)
	}
}
