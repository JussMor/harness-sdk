package tokenizers

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// TiktokenTokenizer uses tiktoken — the same BPE tokenizer family used by
// OpenAI and Anthropic models — for exact token counts, encoding, and decoding.
//
// Encoding options:
//   - CL100K: Claude 3+, GPT-4, GPT-3.5 → use for most production workloads
//   - O200K:  GPT-4o, o1, o3 series
//
// Safe for concurrent use. Codec lazy-loaded on first call.
type TiktokenTokenizer struct {
	encoding string
	once     sync.Once
	codec    tokenizer.Codec
	err      error
}

func NewTiktoken() *TiktokenTokenizer {
	return &TiktokenTokenizer{encoding: "cl100k_base"}
}

func NewTiktokenO200K() *TiktokenTokenizer {
	return &TiktokenTokenizer{encoding: "o200k_base"}
}

func (t *TiktokenTokenizer) load() {
	var enc tokenizer.Encoding
	switch t.encoding {
	case "o200k_base":
		enc = tokenizer.O200kBase
	default:
		enc = tokenizer.Cl100kBase
	}
	t.codec, t.err = tokenizer.Get(enc)
}

// Count returns the exact BPE token count. Falls back to ClaudeTokenizer on error.
func (t *TiktokenTokenizer) Count(text string) int {
	if text == "" {
		return 0
	}
	t.once.Do(t.load)
	if t.err != nil || t.codec == nil {
		return ClaudeTokenizer{}.Count(text)
	}
	ids, _, err := t.codec.Encode(text)
	if err != nil {
		return ClaudeTokenizer{}.Count(text)
	}
	return len(ids)
}

// Encode returns BPE token IDs. Returns nil on error.
func (t *TiktokenTokenizer) Encode(text string) []int {
	if text == "" {
		return nil
	}
	t.once.Do(t.load)
	if t.err != nil || t.codec == nil {
		return nil
	}
	ids, _, err := t.codec.Encode(text)
	if err != nil {
		return nil
	}
	result := make([]int, len(ids))
	for i, id := range ids {
		result[i] = int(id)
	}
	return result
}

// Decode converts token IDs back to text. Returns "" on error.
func (t *TiktokenTokenizer) Decode(tokens []int) string {
	if len(tokens) == 0 {
		return ""
	}
	t.once.Do(t.load)
	if t.err != nil || t.codec == nil {
		return ""
	}
	ids := make([]uint, len(tokens))
	for i, id := range tokens {
		ids[i] = uint(id)
	}
	text, err := t.codec.Decode(ids)
	if err != nil {
		return ""
	}
	return text
}

var _ autobuild.Tokenizer = (*TiktokenTokenizer)(nil)
