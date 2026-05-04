package autobuild

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// TraceID is a unique identifier for an end-to-end conversation flow.
// Same TraceID flows from main agent through subagents through tool calls.
type TraceID string

// SpanID identifies a single operation within a trace (one phase, one tool
// call, one subagent run).
type SpanID string

// NewTraceID creates a random trace ID.
func NewTraceID() TraceID {
	return TraceID("tr_" + randomHex(8))
}

// NewSpanID creates a random span ID.
func NewSpanID() SpanID {
	return SpanID("sp_" + randomHex(6))
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Span is a single timed operation within a trace.
type Span struct {
	TraceID    TraceID                `json:"trace_id"`
	SpanID     SpanID                 `json:"span_id"`
	ParentID   SpanID                 `json:"parent_id,omitempty"`
	Name       string                 `json:"name"`           // "orientation", "tool:web_search", "subagent:research"
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time,omitempty"`
	Duration   time.Duration          `json:"duration_ms"`
	Attributes map[string]any         `json:"attributes,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Status     SpanStatus             `json:"status"`
}

// SpanStatus is the outcome of a span.
type SpanStatus string

const (
	SpanStatusOK    SpanStatus = "ok"
	SpanStatusError SpanStatus = "error"
)

// Tracer collects spans for a single trace. Add it to ctx via WithTracer
// and child operations will attach automatically.
type Tracer struct {
	traceID TraceID
	mu      sync.Mutex
	spans   []Span
}

// NewTracer starts a fresh tracer with a new trace ID.
func NewTracer() *Tracer {
	return &Tracer{traceID: NewTraceID()}
}

// TraceID returns this tracer's trace ID.
func (t *Tracer) TraceID() TraceID { return t.traceID }

// Spans returns all completed spans (defensive copy).
func (t *Tracer) Spans() []Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Span, len(t.spans))
	copy(out, t.spans)
	return out
}

// Start opens a new span and returns it plus a finalize function. Caller
// must call the finalize function (typically in a defer) to record duration
// and emit the span.
func (t *Tracer) Start(name string, parent SpanID, attrs map[string]any) (*Span, func(error)) {
	span := &Span{
		TraceID:    t.traceID,
		SpanID:     NewSpanID(),
		ParentID:   parent,
		Name:       name,
		StartTime:  time.Now(),
		Attributes: attrs,
		Status:     SpanStatusOK,
	}
	finalize := func(err error) {
		span.EndTime = time.Now()
		span.Duration = span.EndTime.Sub(span.StartTime)
		if err != nil {
			span.Error = err.Error()
			span.Status = SpanStatusError
		}
		t.mu.Lock()
		t.spans = append(t.spans, *span)
		t.mu.Unlock()
	}
	return span, finalize
}

// ── Context plumbing ─────────────────────────────────────────────────────────

type tracerKey struct{}
type currentSpanKey struct{}

// WithTracer attaches a Tracer to ctx. Child operations retrieve it via FromContext.
func WithTracer(ctx context.Context, tracer *Tracer) context.Context {
	return context.WithValue(ctx, tracerKey{}, tracer)
}

// TracerFromContext returns the tracer attached to ctx, or nil if none.
func TracerFromContext(ctx context.Context) *Tracer {
	t, _ := ctx.Value(tracerKey{}).(*Tracer)
	return t
}

// WithCurrentSpan attaches the active span ID for parent-child linking.
func WithCurrentSpan(ctx context.Context, spanID SpanID) context.Context {
	return context.WithValue(ctx, currentSpanKey{}, spanID)
}

// CurrentSpan returns the active span ID from ctx, or empty.
func CurrentSpan(ctx context.Context) SpanID {
	id, _ := ctx.Value(currentSpanKey{}).(SpanID)
	return id
}

// StartSpan is a convenience for the common pattern: get tracer from ctx,
// start a span as child of the current span, return new ctx + finalize fn.
//
// Usage:
//
//	ctx, finish := autobuild.StartSpan(ctx, "orientation", nil)
//	defer finish(nil)
//	// ... work ...
func StartSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, func(error)) {
	tracer := TracerFromContext(ctx)
	if tracer == nil {
		return ctx, func(error) {}
	}
	parent := CurrentSpan(ctx)
	span, finalize := tracer.Start(name, parent, attrs)
	newCtx := WithCurrentSpan(ctx, span.SpanID)
	return newCtx, finalize
}
