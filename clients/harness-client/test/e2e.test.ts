// E2E smoke test: drives a HarnessSession against a hand-crafted SSE
// backend that emits the full happy-path event sequence — including an
// interrupt round-trip, a generative-UI component artifact, and the AG-UI
// adapter translation. This validates the wire contract end-to-end without
// requiring an actual LLM.

import { describe, expect, it, vi } from "vitest";
import { toAGUIEvents } from "../src/adapters/agui.js";
import type { Artifact, InterruptRequest, StreamEvent } from "../src/index.js";
import { connect } from "../src/session.js";

function makeSSE(
  events: Array<{ event: string; data: unknown }>,
): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  const body = events
    .map((e) => `event: ${e.event}\ndata: ${JSON.stringify(e.data)}\n\n`)
    .join("");
  return new ReadableStream({
    start(controller) {
      controller.enqueue(enc.encode(body));
      controller.close();
    },
  });
}

describe("HarnessSession E2E", () => {
  it("delivers delta, interrupt, artifact and done events with proper typing", async () => {
    const interruptReq: InterruptRequest = {
      id: "apr_xyz",
      kind: "approval",
      reason: "Tool needs approval",
      created_at: new Date().toISOString(),
      approval: {
        tool_call: { name: "bash", args: { cmd: "ls" } } as never,
      },
    };
    const componentArtifact: Artifact = {
      id: "art_1",
      thread_id: "t1",
      message_id: "m1",
      kind: "component",
      created_at: new Date().toISOString(),
      component: {
        name: "PatientChart",
        catalog_id: "healthcare/v1",
        props: { patientId: "p123", name: "Jane Doe", age: 34 },
      },
    } as Artifact;

    const stream = makeSSE([
      { event: "delta", data: { type: "delta", delta: "Hello " } },
      { event: "delta", data: { type: "delta", delta: "world" } },
      {
        event: "interrupt_required",
        data: { type: "interrupt_required", interrupt: interruptReq },
      },
      {
        event: "interrupt_resolved",
        data: {
          type: "interrupt_resolved",
          interrupt: { ...interruptReq, id: "apr_xyz" },
        },
      },
      {
        event: "artifact_created",
        data: { type: "artifact_created", artifact: componentArtifact },
      },
      { event: "done", data: { type: "done", final: { run_id: "run_1" } } },
    ]);

    const fetchMock = vi.fn(async () => new Response(stream, { status: 200 }));

    const session = connect({
      url: "http://localhost/test",
      message: "hi",
      fetch: fetchMock as unknown as typeof fetch,
    });

    const received: StreamEvent[] = [];
    session.on("delta", (e) => received.push(e));
    let interrupt: InterruptRequest | null = null;
    session.on("interrupt_required", (e) => {
      if (e.interrupt) interrupt = e.interrupt;
    });
    let artifact: Artifact | null = null;
    session.on("artifact_created", (e) => {
      if (e.artifact) artifact = e.artifact;
    });

    await session.done();

    // Wire format assertions
    expect(fetchMock).toHaveBeenCalledOnce();
    const reqInit = fetchMock.mock.calls[0]![1] as RequestInit;
    expect(
      (reqInit.headers as Record<string, string>)["X-Harness-Protocol"],
    ).toBe("1");

    expect(received.map((e) => e.delta).join("")).toBe("Hello world");
    expect(interrupt).not.toBeNull();
    expect(interrupt!.id).toBe("apr_xyz");
    expect(interrupt!.kind).toBe("approval");
    expect(artifact).not.toBeNull();
    expect(artifact!.kind).toBe("component");
    expect(artifact!.component?.name).toBe("PatientChart");
  });

  it("translates StreamEvents to AG-UI events", () => {
    const ev: StreamEvent = {
      type: "delta",
      delta: "hello",
    } as StreamEvent;

    const out = toAGUIEvents(ev, { messageId: "m1" });
    expect(out).toEqual([
      { type: "TEXT_MESSAGE_CONTENT", messageId: "m1", delta: "hello" },
    ]);

    const interrupt: StreamEvent = {
      type: "interrupt_required",
      interrupt: {
        id: "qst_1",
        kind: "question",
        question: { prompt: "Which?", choices: ["a", "b"], multi: false },
      } as InterruptRequest,
    } as StreamEvent;
    const out2 = toAGUIEvents(interrupt);
    expect(out2[0]).toMatchObject({
      type: "HUMAN_INPUT_REQUIRED",
      interruptId: "qst_1",
      kind: "question",
    });

    const art: StreamEvent = {
      type: "artifact_created",
      artifact: {
        id: "a1",
        kind: "component",
        component: { name: "PatientChart", props: { x: 1 } },
      } as Artifact,
    } as StreamEvent;
    const out3 = toAGUIEvents(art);
    expect(out3[0]).toMatchObject({
      type: "GENERATIVE_UI",
      artifactId: "a1",
      componentName: "PatientChart",
    });
  });
});
