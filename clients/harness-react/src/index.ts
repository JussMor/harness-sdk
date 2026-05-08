// @harness/react — React bindings for @harness/client.
//
// All wire types are re-exported from the generated client so apps only
// depend on @harness/react and stay in sync with the Go SDK automatically.

export {
  ArtifactRenderer,
  type ArtifactRendererProps,
  type ComponentCatalog,
  type ComponentCatalogEntry,
} from "./ArtifactRenderer.js";
export { useArtifacts } from "./useArtifacts.js";
export {
  useHarness,
  type UseHarnessOptions,
  type UseHarnessState,
} from "./useHarness.js";
export { useInterrupts, type PendingInterrupt } from "./useInterrupts.js";

// Re-export the wire surface so app code only imports from @harness/react.
export {
  ProtocolVersion,
  StreamEventNames,
  type ArtifactKindName,
  type InterruptKindName,
  type StreamEventName,
} from "@harness/client";
export type {
  ApprovalPayload,
  ApprovalRequest,
  Artifact,
  ComponentArtifact,
  FileArtifact,
  FormPayload,
  InterruptRequest,
  InterruptResponse,
  QuestionPayload,
  StreamEvent,
} from "@harness/client";
