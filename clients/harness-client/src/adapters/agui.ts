// AG-UI compatibility adapter.
//
// AG-UI is the CopilotKit-style frontend event protocol used by tools like
// @copilotkit/runtime and @ag-ui/client. This adapter translates our wire
// StreamEvents into the AG-UI shape so apps already wired to AG-UI can
// consume Harness sessions without rewriting their renderer.
//
// Mapping (intentionally conservative — only the events with a clear AG-UI
// counterpart are translated; the rest pass through as RAW for app-level
// handling):
//
//   StreamEvent.type         AG-UI event
//   ──────────────────────   ───────────────────────────
//   delta                    TEXT_MESSAGE_CONTENT
//   thinking                 THINKING
//   tool_call                TOOL_CALL_START + TOOL_CALL_ARGS
//   tool_result              TOOL_CALL_END
//   interrupt_required       HUMAN_INPUT_REQUIRED
//   interrupt_resolved       HUMAN_INPUT_RESOLVED
//   artifact_created         GENERATIVE_UI
//   artifact_updated         GENERATIVE_UI
//   done                     RUN_FINISHED
//   error                    RUN_ERROR
//
// Reference: https://docs.copilotkit.ai/concepts/agent-events

import type { StreamEvent } from "../generated/index.js";

/** Discriminated union of AG-UI events emitted by the adapter. */
export type AGUIEvent =
  | { type: "TEXT_MESSAGE_CONTENT"; messageId: string; delta: string }
  | { type: "THINKING"; messageId: string; delta: string }
  | {
      type: "TOOL_CALL_START";
      toolCallId: string;
      toolName: string;
    }
  | { type: "TOOL_CALL_ARGS"; toolCallId: string; args: unknown }
  | {
      type: "TOOL_CALL_END";
      toolCallId: string;
      result?: string;
      error?: boolean;
    }
  | {
      type: "HUMAN_INPUT_REQUIRED";
      interruptId: string;
      kind: "approval" | "question" | "form_input" | string;
      payload: unknown;
    }
  | { type: "HUMAN_INPUT_RESOLVED"; interruptId: string }
  | {
      type: "GENERATIVE_UI";
      artifactId: string;
      componentName: string;
      props: unknown;
    }
  | { type: "RUN_FINISHED"; runId?: string }
  | { type: "RUN_ERROR"; message: string }
  | { type: "RAW"; event: StreamEvent };

export interface ToAGUIOptions {
  /** Stable message id for a given assistant turn. Defaults to "msg". */
  messageId?: string;
}

/**
 * Translate a single Harness StreamEvent into zero or more AG-UI events.
 * The function is pure — it does not maintain cross-call state. Callers
 * that need a single message id across delta chunks should pass `opts.messageId`.
 */
export function toAGUIEvents(
  ev: StreamEvent,
  opts: ToAGUIOptions = {},
): AGUIEvent[] {
  const messageId = opts.messageId ?? "msg";

  switch (ev.type) {
    case "delta":
      return ev.delta
        ? [{ type: "TEXT_MESSAGE_CONTENT", messageId, delta: ev.delta }]
        : [];
    case "thinking":
      return ev.thinking
        ? [{ type: "THINKING", messageId, delta: ev.thinking }]
        : [];
    case "tool_call": {
      const tc = ev.tool_call;
      if (!tc) return [];
      const id = (tc as { id?: string }).id ?? `tc_${Date.now()}`;
      return [
        {
          type: "TOOL_CALL_START",
          toolCallId: id,
          toolName: (tc as { name?: string }).name ?? "",
        },
        {
          type: "TOOL_CALL_ARGS",
          toolCallId: id,
          args: (tc as { args?: unknown }).args,
        },
      ];
    }
    case "tool_result": {
      const tr = ev.tool_result;
      if (!tr) return [];
      const id = (tr as { id?: string }).id ?? `tc_${Date.now()}`;
      return [
        {
          type: "TOOL_CALL_END",
          toolCallId: id,
          result: (tr as { content?: string }).content,
          error: (tr as { error?: boolean }).error,
        },
      ];
    }
    case "interrupt_required": {
      const ir = ev.interrupt;
      if (!ir) return [];
      return [
        {
          type: "HUMAN_INPUT_REQUIRED",
          interruptId: ir.id,
          kind: ir.kind,
          payload:
            ir.kind === "approval"
              ? ir.approval
              : ir.kind === "question"
                ? ir.question
                : ir.form,
        },
      ];
    }
    case "interrupt_resolved": {
      const ir = ev.interrupt;
      if (!ir) return [];
      return [{ type: "HUMAN_INPUT_RESOLVED", interruptId: ir.id }];
    }
    case "artifact_created":
    case "artifact_updated": {
      const a = ev.artifact;
      if (!a || a.kind !== "component" || !a.component) return [];
      return [
        {
          type: "GENERATIVE_UI",
          artifactId: a.id,
          componentName: a.component.name,
          props: a.component.props,
        },
      ];
    }
    case "done":
      return [
        {
          type: "RUN_FINISHED",
          runId: (ev.final as { run_id?: string } | undefined)?.run_id,
        },
      ];
    case "error":
      return [
        {
          type: "RUN_ERROR",
          message: (ev as { error?: string }).error ?? "stream error",
        },
      ];
    default:
      return [{ type: "RAW", event: ev }];
  }
}

/**
 * Helper that drives a HarnessSession and yields AG-UI events as an async
 * iterator — convenient for piping into AG-UI consumers.
 */
export async function* streamAGUI(
  source: AsyncIterable<StreamEvent>,
  opts: ToAGUIOptions = {},
): AsyncGenerator<AGUIEvent> {
  for await (const ev of source) {
    for (const out of toAGUIEvents(ev, opts)) {
      yield out;
    }
  }
}
