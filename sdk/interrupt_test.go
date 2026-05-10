package autobuild

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestInterruptGate_ApprovalRoundTrip exercises an Approval interrupt
// directly on InterruptGate.
func TestInterruptGate_ApprovalRoundTrip(t *testing.T) {
	gate := NewInterruptGate(4)

	go func() {
		req := <-gate.Requests()
		if req.ID == "" || req.Kind != InterruptKindApproval || req.Approval == nil || req.Approval.ToolCall.Name != "bash" {
			t.Errorf("unexpected request: %#v", req)
		}
		if !gate.Respond(InterruptResponse{ID: req.ID, Approved: true}) {
			t.Errorf("respond returned false")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := gate.Wait(ctx, InterruptRequest{
		ID:        "apr_test",
		Kind:      InterruptKindApproval,
		Reason:    "test",
		CreatedAt: time.Now(),
		Approval:  &ApprovalPayload{ToolCall: ToolCallEntry{Name: "bash", Arguments: `{"cmd":"ls"}`}},
	})
	if err != nil {
		t.Fatalf("wait error: %v", err)
	}
	if !resp.Approved {
		t.Fatalf("expected approval, got %#v", resp)
	}
}

// TestInterruptGate_QuestionRoundTrip exercises the generalized InterruptGate.
func TestInterruptGate_QuestionRoundTrip(t *testing.T) {
	gate := NewInterruptGate(2)

	go func() {
		req := <-gate.Requests()
		if req.Kind != InterruptKindQuestion || req.Question == nil {
			t.Errorf("unexpected request: %#v", req)
			return
		}
		ans, _ := json.Marshal(map[string]string{"choice": "yes"})
		gate.Respond(InterruptResponse{ID: req.ID, Approved: true, Answer: ans})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := gate.Wait(ctx, InterruptRequest{
		Kind:     InterruptKindQuestion,
		Reason:   "Pick one",
		Question: &QuestionPayload{Prompt: "yes/no?", Choices: []string{"yes", "no"}},
	})
	if err != nil {
		t.Fatalf("wait error: %v", err)
	}
	if !resp.Approved || len(resp.Answer) == 0 {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

// TestInterruptGate_ContextCancel ensures Wait returns when ctx expires.
func TestInterruptGate_ContextCancel(t *testing.T) {
	gate := NewInterruptGate(1)
	go func() {
		for range gate.Requests() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := gate.Wait(ctx, InterruptRequest{
		Kind:     InterruptKindQuestion,
		Question: &QuestionPayload{Prompt: "anyone there?"},
	})
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

// TestInterruptGate_ResolutionToken signs a token, redeems it via the inbound
// resolution path, and verifies tampered or expired tokens are rejected.
func TestInterruptGate_ResolutionToken(t *testing.T) {
	gate := NewInterruptGate(1)

	done := make(chan InterruptResponse, 1)
	go func() {
		<-gate.Requests()
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, err := gate.Wait(ctx, InterruptRequest{
			ID:       "int_token",
			Kind:     InterruptKindApproval,
			Approval: &ApprovalPayload{ToolCall: ToolCallEntry{Name: "bash"}},
		})
		if err == nil {
			done <- resp
		}
	}()
	time.Sleep(20 * time.Millisecond)

	tok, err := gate.IssueResolutionToken("int_token", time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := gate.ResolveByToken(tok, InterruptResponse{Approved: true}); err != nil {
		t.Fatalf("resolve by token: %v", err)
	}
	select {
	case resp := <-done:
		if !resp.Approved {
			t.Fatalf("expected approved")
		}
	case <-time.After(time.Second):
		t.Fatalf("waiter did not return")
	}

	// Tampered signature must fail.
	bad := tok[:len(tok)-2] + "xx"
	if err := gate.ResolveByToken(bad, InterruptResponse{Approved: true}); err == nil {
		t.Fatalf("expected signature mismatch")
	}

	// Expired token must fail.
	expired, _ := gate.IssueResolutionToken("int_token", -time.Second)
	if err := gate.ResolveByToken(expired, InterruptResponse{Approved: true}); err == nil {
		t.Fatalf("expected expired error")
	}
}

// TestArtifactEmitter ensures EmitArtifact funnels through ctx and is a no-op
// when no emitter is installed.
func TestArtifactEmitter(t *testing.T) {
	ctx := context.Background()
	if EmitArtifact(ctx, Artifact{Kind: ArtifactKindFile}) {
		t.Fatalf("expected no-op without emitter")
	}

	var captured Artifact
	ctx = WithArtifactEmitter(ctx, func(a Artifact) { captured = a })

	art, err := NewComponentArtifact("PatientChart", map[string]any{"patientId": "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if !EmitArtifact(ctx, art) {
		t.Fatalf("expected emit to succeed")
	}
	if captured.Kind != ArtifactKindComponent || captured.Component.Name != "PatientChart" {
		t.Fatalf("unexpected captured artifact: %#v", captured)
	}
	if len(captured.Component.Props) == 0 {
		t.Fatalf("expected props to round-trip through json")
	}
}
