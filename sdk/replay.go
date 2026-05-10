package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Replay re-runs a stored Conversation against the current Runtime and
// reports differences between the original responses and the new ones.
// Use for regression testing: capture conversations from production,
// replay against new model versions or SDK changes to see drift.
//
// Replay does not call write side-effects: memory writes, checkpoints,
// and tool calls go through the same Runtime configuration as production —
// configure with a sandboxed Engine if you want isolated replay.
type Replay struct {
	// Runtime is the Runtime to replay against.
	Runtime *Runtime

	// CompareMode controls how original vs replay outputs are compared.
	CompareMode ReplayCompareMode
}

// ReplayCompareMode is how replay compares original vs new outputs.
type ReplayCompareMode string

const (
	// ReplayCompareExact requires byte-for-byte match.
	ReplayCompareExact ReplayCompareMode = "exact"

	// ReplayCompareNormalized strips whitespace and lowercases before compare.
	// Tolerates cosmetic differences in output.
	ReplayCompareNormalized ReplayCompareMode = "normalized"

	// ReplayCompareLength reports diff in length only.
	// Useful when LLM nondeterminism makes exact comparison meaningless.
	ReplayCompareLength ReplayCompareMode = "length"
)

// ReplayResult is the outcome of replaying one conversation.
type ReplayResult struct {
	ConversationID string         `json:"conversation_id"`
	Turns          []ReplayTurn   `json:"turns"`
	TotalDrift     int            `json:"total_drift"`     // turns that differed
	StartedAt      time.Time      `json:"started_at"`
	Duration       time.Duration  `json:"duration_ms"`
}

// ReplayTurn is the comparison for a single turn in the replay.
type ReplayTurn struct {
	Index            int    `json:"index"`
	UserMessage      string `json:"user_message"`
	OriginalResponse string `json:"original_response,omitempty"`
	ReplayResponse   string `json:"replay_response,omitempty"`
	Match            bool   `json:"match"`
	Notes            string `json:"notes,omitempty"`
}

// Run replays the original conversation. The original Conversation is
// not mutated. The Runtime drives a fresh Conversation through each
// user message in order.
func (r *Replay) Run(ctx context.Context, original *Conversation) (*ReplayResult, error) {
	if r.Runtime == nil {
		return nil, fmt.Errorf("replay: runtime is required")
	}
	if original == nil {
		return nil, fmt.Errorf("replay: original conversation is required")
	}

	result := &ReplayResult{
		ConversationID: original.ID,
		StartedAt:      time.Now(),
	}

	fresh := NewConversation(original.ID + "-replay")
	turnIdx := 0
	for i, msg := range original.Messages {
		if msg.Role != RoleUser {
			continue
		}
		// Find the matching original assistant response (next assistant after this user)
		var originalResp string
		for j := i + 1; j < len(original.Messages); j++ {
			if original.Messages[j].Role == RoleAssistant {
				originalResp = original.Messages[j].Content
				break
			}
			if original.Messages[j].Role == RoleUser {
				break // no assistant response between two users
			}
		}

		rr, err := r.Runtime.Run(ctx, fresh, msg.Content)
		turn := ReplayTurn{
			Index:            turnIdx,
			UserMessage:      msg.Content,
			OriginalResponse: originalResp,
		}
		turnIdx++

		if err != nil {
			turn.Notes = "replay error: " + err.Error()
			turn.Match = false
			result.Turns = append(result.Turns, turn)
			result.TotalDrift++
			continue
		}
		turn.ReplayResponse = rr.Response
		turn.Match = r.compare(originalResp, rr.Response)
		if !turn.Match {
			result.TotalDrift++
		}
		result.Turns = append(result.Turns, turn)
	}

	result.Duration = time.Since(result.StartedAt)
	return result, nil
}

func (r *Replay) compare(original, replay string) bool {
	switch r.CompareMode {
	case ReplayCompareNormalized:
		return normalize(original) == normalize(replay)
	case ReplayCompareLength:
		return abs(len(original)-len(replay)) < 50
	default:
		return original == replay
	}
}

func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ── Snapshot testing ─────────────────────────────────────────────────────────

// Snapshot captures the result of a Runtime.Run for later comparison.
// Use for golden-file testing: run once, save snapshot, then on subsequent
// runs compare against the snapshot and fail if drift exceeds threshold.
type Snapshot struct {
	ID            string         `json:"id"`
	Input         string         `json:"input"`
	Response      string         `json:"response"`
	Turns         int            `json:"turns"`
	Usage         TokenUsage     `json:"usage"`
	StopReason    string         `json:"stop_reason"`
	MemoryWritten []string       `json:"memory_written,omitempty"`
	CapturedAt    time.Time      `json:"captured_at"`
}

// CaptureSnapshot runs the input against the Runtime and saves the result
// as a Snapshot. Save the returned snapshot to disk via SaveSnapshot.
func CaptureSnapshot(ctx context.Context, rt *Runtime, id, input string) (*Snapshot, error) {
	conv := NewConversation("snapshot-" + id)
	rr, err := rt.Run(ctx, conv, input)
	if err != nil {
		return nil, fmt.Errorf("snapshot capture: %w", err)
	}
	return &Snapshot{
		ID:            id,
		Input:         input,
		Response:      rr.Response,
		Turns:         rr.Turns,
		Usage:         rr.Usage,
		StopReason:    rr.StopReason,
		MemoryWritten: rr.MemoryWritten,
		CapturedAt:    time.Now(),
	}, nil
}

// SaveSnapshot writes a snapshot to a JSON file.
func SaveSnapshot(snap *Snapshot, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadSnapshot reads a snapshot from a JSON file.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// SnapshotDiff describes how a current run differs from a saved snapshot.
type SnapshotDiff struct {
	IDsMatch          bool          `json:"ids_match"`
	InputsMatch       bool          `json:"inputs_match"`
	ResponseExact     bool          `json:"response_exact"`
	ResponseNormalized bool         `json:"response_normalized"`
	LengthDelta       int           `json:"length_delta"`
	TurnsDelta        int           `json:"turns_delta"`
	UsageDelta        TokenUsage    `json:"usage_delta"`
}

// CompareSnapshot compares a current Runtime result against a saved snapshot.
// Returns a structured diff. Decide pass/fail based on which fields drift.
func CompareSnapshot(snap *Snapshot, rr *RuntimeResult, input string) SnapshotDiff {
	diff := SnapshotDiff{
		IDsMatch:           true, // placeholder — caller knows the IDs
		InputsMatch:        snap.Input == input,
		ResponseExact:      snap.Response == rr.Response,
		ResponseNormalized: normalize(snap.Response) == normalize(rr.Response),
		LengthDelta:        len(rr.Response) - len(snap.Response),
		TurnsDelta:         rr.Turns - snap.Turns,
		UsageDelta: TokenUsage{
			PromptTokens:     rr.Usage.PromptTokens - snap.Usage.PromptTokens,
			CompletionTokens: rr.Usage.CompletionTokens - snap.Usage.CompletionTokens,
			TotalTokens:      rr.Usage.TotalTokens - snap.Usage.TotalTokens,
		},
	}
	return diff
}

func diffStrings(a, b []string) []string {
	bset := make(map[string]bool, len(b))
	for _, s := range b {
		bset[s] = true
	}
	var out []string
	for _, s := range a {
		if !bset[s] {
			out = append(out, s)
		}
	}
	return out
}
