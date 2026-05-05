package tokenizers

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// TiktokenTokenizer uses tiktoken — the same BPE tokenizer family used by
// OpenAI and Anthropic models — for accurate token counts.
//
// Encoding options:
//   - CL100K: Claude 3+, GPT-4, GPT-3.5 → use for most production workloads
//   - O200K:  GPT-4o → use if routing to o-series models
//
// TiktokenTokenizer is safe for concurrent use.
// The underlying codec is lazy-loaded on first call and shared across instances.
type TiktokenTokenizer struct {
	encoding string
	once     sync.Once
	codec    tokenizer.Codec
	err      error
}

// NewTiktoken returns a TiktokenTokenizer with CL100K encoding
// (the right choice for Claude 3+ and GPT-4 family).
func NewTiktoken() *TiktokenTokenizer {
	return &TiktokenTokenizer{encoding: "cl100k_base"}
}

// NewTiktokenO200K returns a TiktokenTokenizer with O200K encoding
// (GPT-4o, o1, o3 series).
func NewTiktokenO200K() *TiktokenTokenizer {
	return &TiktokenTokenizer{encoding: "o200k_base"}
}

// Count returns the exact BPE token count for text.
// Falls back to the ClaudeTokenizer heuristic if tiktoken fails to load.
func (t *TiktokenTokenizer) Count(text string) int {
	if text == "" {
		return 0
	}

	t.once.Do(func() {
		var enc tokenizer.Encoding
		switch t.encoding {
		case "o200k_base":
			enc = tokenizer.O200kBase
		default:
			enc = tokenizer.Cl100kBase
		}
		t.codec, t.err = tokenizer.Get(enc)
	})

	if t.err != nil || t.codec == nil {
		// Fallback to ClaudeTokenizer heuristic — better than nothing
		return (&ClaudeTokenizer{}).Count(text)
	}

	ids, _, err := t.codec.Encode(text)
	if err != nil {
		return (&ClaudeTokenizer{}).Count(text)
	}
	return len(ids)
}

// Verify TiktokenTokenizer implements the SDK interface.
var _ autobuild.Tokenizer = (*TiktokenTokenizer)(nil)
