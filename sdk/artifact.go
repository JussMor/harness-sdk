package autobuild

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ── Artifacts ─────────────────────────────────────────────────────────────────
//
// An Artifact is a typed payload the agent attaches to a turn that is
// rendered separately from chat text. Two kinds are supported on the wire:
//
//   ArtifactKindFile      — a generated file (code, html, markdown, etc.).
//                            Existing backend persistence layers (e.g.
//                            example/backend-chat/artifact_*) own the file
//                            content and versioning; the SDK only describes
//                            the on-the-wire shape.
//
//   ArtifactKindComponent — a generative-UI component the frontend should
//                            render. The agent picks a component name from
//                            the catalog the client registered and supplies
//                            props validated against the catalog's schema.
//                            Optionally embeds an A2UI v0.8 surface for
//                            fully declarative UIs.
//
// Artifacts flow through StreamEventArtifactCreated / Updated. Tools and
// skills emit them by calling EmitArtifact(ctx, artifact). The runtime
// installs an emitter on ctx in RunStream; outside of RunStream, EmitArtifact
// is a no-op so tools degrade gracefully.

// ArtifactKind discriminates between Artifact variants.
type ArtifactKind string

const (
	ArtifactKindFile      ArtifactKind = "file"
	ArtifactKindComponent ArtifactKind = "component"
)

// ArtifactPlacement tells the frontend where to mount the artifact:
//
//   ArtifactPlacementCanvas — render in the dedicated artifact panel
//                              (default; same surface as files).
//   ArtifactPlacementInline — render inline within the chat transcript,
//                              attached to the current assistant turn.
//
// Frontend renderers MAY ignore this hint, but apps that distinguish a
// "canvas" surface from inline chat content should honor it.
type ArtifactPlacement string

const (
	ArtifactPlacementCanvas ArtifactPlacement = "canvas"
	ArtifactPlacementInline ArtifactPlacement = "inline"
)

// ArtifactInteraction wires a generative-UI artifact to a pending interrupt
// so the rendered component can post user input back to the agent loop.
//
// Token is an HMAC-signed resolution token issued by InterruptGate; the
// frontend POSTs the form data to /api/interrupts/:token/resolve.
// ChatID identifies which session's gate the token resolves against.
//
// When this field is non-nil, renderers should pass an `onSubmit(data)`
// prop to the component; calling it sends `data` as the interrupt answer.
type ArtifactInteraction struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id,omitempty"`
}

// Artifact is the wire representation of a generative output the agent
// attaches to a turn. Exactly one of File / Component is set, matching Kind.
type Artifact struct {
	ID        string            `json:"id"`
	ThreadID  string            `json:"thread_id,omitempty"`
	MessageID string            `json:"message_id,omitempty"`
	Kind      ArtifactKind      `json:"kind"`
	Placement ArtifactPlacement `json:"placement,omitempty"`
	CreatedAt time.Time         `json:"created_at"`

	File      *FileArtifact      `json:"file,omitempty"`
	Component *ComponentArtifact `json:"component,omitempty"`

	// Interaction wires the artifact to a pending interrupt so the rendered
	// component can return user input to the agent. Set when Kind=component
	// and the agent is awaiting form data via RequestInterrupt.
	Interaction *ArtifactInteraction `json:"interaction,omitempty"`
}

// FileArtifact describes a generated file. Content is inline for small
// payloads; large files should be stored externally and referenced by URL.
type FileArtifact struct {
	Title    string `json:"title,omitempty"`
	Language string `json:"language,omitempty"` // e.g. "typescript", "markdown"
	Path     string `json:"path,omitempty"`
	Content  string `json:"content,omitempty"`
	URL      string `json:"url,omitempty"`     // optional CDN/S3 link
	Version  int    `json:"version,omitempty"` // optional version counter
}

// ComponentArtifact describes a generative-UI component to render.
//
// Name is looked up in the frontend's catalog. CatalogID disambiguates
// between concurrent catalogs (e.g. "healthcare-v1" vs "default").
// Props are validated against the catalog's JSON Schema before render.
//
// A2UISurface is an optional A2UI v0.8 surface document; when present, a
// renderer that supports A2UI may use it instead of the named component
// for fully declarative UIs.
type ComponentArtifact struct {
	Name        string          `json:"name"`
	CatalogID   string          `json:"catalog_id,omitempty"`
	Props       json.RawMessage `json:"props,omitempty"`
	A2UISurface json.RawMessage `json:"a2ui_surface,omitempty"`
}

// NewComponentArtifact constructs a Component artifact with a fresh ID.
// props is marshaled to JSON; pass nil for component-without-props cases.
func NewComponentArtifact(name string, props any) (Artifact, error) {
	if name == "" {
		return Artifact{}, errors.New("component name required")
	}
	var raw json.RawMessage
	if props != nil {
		b, err := json.Marshal(props)
		if err != nil {
			return Artifact{}, err
		}
		raw = b
	}
	return Artifact{
		ID:        "art_" + randomHex(8),
		Kind:      ArtifactKindComponent,
		CreatedAt: time.Now(),
		Component: &ComponentArtifact{Name: name, Props: raw},
	}, nil
}

// ── Emitter (context-scoped) ─────────────────────────────────────────────────

// ArtifactEmitter is what Skills and Tools call to attach an artifact to the
// current streaming turn. The runtime installs an emitter on ctx in
// RunStream; outside of streaming RunStream, emitters are absent and
// EmitArtifact is a no-op.
type ArtifactEmitter func(Artifact)

type artifactEmitterCtxKey struct{}

// WithArtifactEmitter stores an emitter on ctx for downstream tools to use.
// Internal: callers should not need this — the runtime wires it during
// RunStream.
func WithArtifactEmitter(ctx context.Context, emit ArtifactEmitter) context.Context {
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, artifactEmitterCtxKey{}, emit)
}

// ArtifactEmitterFromContext returns the emitter installed on ctx, or nil.
func ArtifactEmitterFromContext(ctx context.Context) ArtifactEmitter {
	v, _ := ctx.Value(artifactEmitterCtxKey{}).(ArtifactEmitter)
	return v
}

// EmitArtifact attaches an artifact to the current streaming turn if an
// emitter is installed on ctx. Outside of RunStream, it is a no-op so tools
// remain testable in isolation.
//
// Returns true if the artifact was emitted, false otherwise.
func EmitArtifact(ctx context.Context, a Artifact) bool {
	emit := ArtifactEmitterFromContext(ctx)
	if emit == nil {
		return false
	}
	if a.ID == "" {
		a.ID = "art_" + randomHex(8)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	emit(a)
	return true
}

// ── Interrupt requester (context-scoped) ─────────────────────────────────────

// InterruptRequester is what tools call to pause the agent loop and wait
// for human input. The runtime installs one on ctx in RunStream pointing at
// the session's InterruptGate. Outside of streaming, requesters are absent
// and RequestInterrupt returns ErrNoInterruptGate.
type InterruptRequester func(ctx context.Context, req InterruptRequest) (InterruptResponse, error)

type interruptRequesterCtxKey struct{}

// ErrNoInterruptGate is returned when RequestInterrupt is called outside of
// a streaming context where no gate is installed.
var ErrNoInterruptGate = errors.New("no interrupt gate installed on context")

// WithInterruptRequester stores a requester on ctx for downstream tools to
// use. Internal: the runtime wires it during RunStream.
func WithInterruptRequester(ctx context.Context, r InterruptRequester) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, interruptRequesterCtxKey{}, r)
}

// InterruptRequesterFromContext returns the requester installed on ctx, or nil.
func InterruptRequesterFromContext(ctx context.Context) InterruptRequester {
	v, _ := ctx.Value(interruptRequesterCtxKey{}).(InterruptRequester)
	return v
}

// RequestInterrupt pauses the agent loop and waits for a human response.
// Returns ErrNoInterruptGate when no gate is installed (e.g. outside RunStream).
func RequestInterrupt(ctx context.Context, req InterruptRequest) (InterruptResponse, error) {
	r := InterruptRequesterFromContext(ctx)
	if r == nil {
		return InterruptResponse{}, ErrNoInterruptGate
	}
	return r(ctx, req)
}
