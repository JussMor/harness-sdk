// Package autobuild provides a minimal, extensible SDK for orchestrating
// agent-driven software delivery workflows.
//
// The SDK codifies the Obvious/Autobuild execution model as composable Go
// interfaces. It is provider-agnostic: no LLM vendor lock-in, no platform
// coupling. Consumers wire their own implementations of each provider
// interface and compose them into an [Engine].
//
// Core abstractions:
//
//   - [WorkflowEngine]   — 6-phase lifecycle (Orientation → Closure)
//   - [ThreadProvider]    — threads, runners, child threads, messaging
//   - [MemoryProvider]    — persistent two-scope memory (user / project)
//   - [ToolRegistry]      — typed tool definitions with JSON Schema
//   - [SkillProvider]     — on-demand knowledge loading with trigger matching
//   - [TaskProvider]      — reusable workflows with steps, gates, conditions, triggers
//   - [ModeProvider]      — execution modes with tool access control
//   - [PlanProvider]      — initiative → executable DAG orchestration
//   - [CheckpointProvider]— safety snapshots before mutations
//   - [SandboxDriver]     — command execution and file I/O
//   - [EventBus]          — publish/subscribe for inter-component notifications
//
// Quick start:
//
//	engine := autobuild.New(
//	    autobuild.WithMemory(myMemory),
//	    autobuild.WithSandbox(mySandbox),
//	    autobuild.WithToolRegistry(myRegistry),
//	)
package autobuild

// Version is the SemVer of the SDK.
const Version = "0.1.0"
