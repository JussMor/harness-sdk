package autobuild

// ── Protocol identity ─────────────────────────────────────────────────────────
//
// This file is the single source of truth for protocol-level constants
// shared between the SDK (Go) and any client (TypeScript, Python, etc.).
//
// IMPORTANT: when you add a new StreamEventType, EventType, or InterruptKind,
// ALWAYS reference its constant here. The cross-language code generator
// (clients/harness-client) reads these declarations to keep clients in sync.
// Bumping ProtocolVersion is required when removing or renaming any wire-level
// constant — adding new ones is backwards-compatible.

// ProtocolVersion is the SemVer-major version of the harness wire protocol
// (events, interrupt requests/responses, artifacts, webhooks). Clients that
// receive a different major version should refuse the stream.
const ProtocolVersion = "1"

// ── Wire event names (cross-language) ─────────────────────────────────────────
//
// These mirror the StreamEventType constants in streaming.go. They are kept
// here as a quick index for code generation tooling. Do NOT duplicate values:
// import the StreamEventType constants in Go code; the generator reads them
// via reflection from streaming.go to produce TypeScript union types.
//
// Stream events emitted by the runtime:
//   delta, thinking, tool_call, tool_result, turn_complete,
//   plan_proposed, subagent_result,
//   interrupt_required, interrupt_resolved,
//   confirmation_required, confirmation_resolved (legacy aliases),
//   artifact_created, artifact_updated,
//   done, error
//
// Internal EventBus events (server-only):
//   agent.loop.started, agent.turn.completed, agent.loop.completed, ...
//   interrupt.requested, interrupt.resolved (companion of stream events)
