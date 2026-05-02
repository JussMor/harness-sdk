package autobuild

// ReasoningStep is a safe, structured execution trace item suitable for UI
// rendering. It is not raw hidden chain-of-thought from the model.
type ReasoningStep struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Title   string   `json:"title"`
	Content string   `json:"content,omitempty"`
	Details []string `json:"details,omitempty"`
}
