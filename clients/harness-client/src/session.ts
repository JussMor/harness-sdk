// HarnessSession — the high-level client API.
//
// Built on top of:
//   - parseSSE (transport.ts) : zero protocol knowledge
//   - generated/* (tygo)       : single source of truth for types/events
//
// The shape is intentionally small so adding a new event in Go does not
// require any change here: clients listen via `session.on(eventName, cb)`
// and TypeScript narrows the payload via the generated event-name union.

import type { StreamEvent } from "./generated/index.js";
import { ProtocolVersion, type StreamEventName } from "./generated/index.js";
import { parseSSE } from "./transport.js";

export interface ConnectOptions {
  /** Backend URL that accepts POST and replies with `text/event-stream`. */
  url: string;
  /** Optional ID of an existing chat/thread to resume. */
  threadId?: string | number;
  /** Initial user message. Sent as the body field `message`. */
  message: string;
  /** Pass `human_in_loop: true` to enable interrupt-driven approvals. */
  humanInLoop?: boolean;
  /** Custom fetch impl (defaults to global fetch). Use to inject auth headers. */
  fetch?: typeof fetch;
  /** Extra headers merged into the POST request. */
  headers?: Record<string, string>;
  /** Abort signal forwarded to fetch. */
  signal?: AbortSignal;
}

type Handler<T extends StreamEventName> = (
  event: Extract<StreamEvent, { type: T }>,
) => void;

export interface HarnessSession {
  /** Subscribe to a wire event by type. Multiple handlers per type are allowed. */
  on<T extends StreamEventName>(type: T, handler: Handler<T>): () => void;
  /** Resolve an interrupt currently awaiting human input. */
  resolveInterrupt(
    chatId: string | number,
    id: string,
    response: { approved?: boolean; answer?: unknown; modifiedArgs?: string },
  ): Promise<void>;
  /** Wait for the stream to terminate (StreamEventDone or error). */
  done(): Promise<void>;
  /** Underlying protocol version negotiated with the server. */
  readonly protocolVersion: string;
}

/**
 * connect opens a streaming chat session. Returns immediately with a session
 * handle whose .on(...) listeners receive events as they arrive.
 */
export function connect(opts: ConnectOptions): HarnessSession {
  const fetchImpl = opts.fetch ?? fetch;
  const handlers = new Map<string, Set<(ev: unknown) => void>>();
  let resolveDone: () => void = () => {};
  let rejectDone: (err: unknown) => void = () => {};
  const donePromise = new Promise<void>((res, rej) => {
    resolveDone = res;
    rejectDone = rej;
  });

  function emit(eventName: string, payload: unknown) {
    const subs = handlers.get(eventName);
    if (!subs) return;
    for (const cb of subs) cb(payload);
  }

  (async () => {
    try {
      const res = await fetchImpl(opts.url, {
        method: "POST",
        signal: opts.signal,
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          "X-Harness-Protocol": ProtocolVersion,
          ...(opts.headers ?? {}),
        },
        body: JSON.stringify({
          thread_id: opts.threadId,
          message: opts.message,
          human_in_loop: opts.humanInLoop ?? false,
        }),
      });

      if (!res.ok || !res.body) {
        throw new Error(
          `harness stream failed: ${res.status} ${res.statusText}`,
        );
      }

      for await (const sseEv of parseSSE(res.body)) {
        let payload: unknown = sseEv.data;
        try {
          payload = JSON.parse(sseEv.data);
        } catch {
          // non-JSON event payload — leave as string
        }
        emit(sseEv.event, payload);
        if (sseEv.event === "done" || sseEv.event === "error") {
          break;
        }
      }
      resolveDone();
    } catch (err) {
      emit("error", { message: (err as Error).message });
      rejectDone(err);
    }
  })();

  return {
    protocolVersion: ProtocolVersion,
    on(type, handler) {
      const set = handlers.get(type) ?? new Set();
      set.add(handler as (ev: unknown) => void);
      handlers.set(type, set);
      return () => set.delete(handler as (ev: unknown) => void);
    },
    async resolveInterrupt(chatId, id, response) {
      const baseUrl = new URL(opts.url);
      const confirmUrl = new URL("/api/confirm", baseUrl);
      const res = await fetchImpl(confirmUrl.toString(), {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(opts.headers ?? {}),
        },
        body: JSON.stringify({
          chat_id: chatId,
          id,
          approved: response.approved ?? false,
          modified_args: response.modifiedArgs ?? "",
          answer: response.answer,
        }),
      });
      if (!res.ok) {
        throw new Error(`resolveInterrupt: ${res.status} ${res.statusText}`);
      }
    },
    done() {
      return donePromise;
    },
  };
}
