// useHarness — primary hook to drive a streaming chat session.
//
// The hook is intentionally event-driven: it subscribes to every event the
// generated `StreamEventNames` union enumerates, so adding a new event in
// the Go SDK automatically flows through to consumers via the typed
// `lastEvent` state and `on()` escape hatch — no React-side change.

import {
  connect,
  StreamEventNames,
  type ConnectOptions,
  type HarnessSession,
  type StreamEvent,
  type StreamEventName,
} from "@harness/client";
import { useCallback, useEffect, useRef, useState } from "react";

export interface UseHarnessOptions extends Omit<ConnectOptions, "message"> {
  /** When false, the hook stays idle until you call `send()`. */
  autoStart?: boolean;
  /** Initial message used by autoStart or the first send() with no arg. */
  initialMessage?: string;
}

export interface UseHarnessState {
  session: HarnessSession | null;
  events: StreamEvent[];
  lastEvent: StreamEvent | null;
  error: Error | null;
  done: boolean;
  /** Open a new session with the given message (replaces any prior one). */
  send: (message?: string) => void;
  /** Subscribe to a specific event type without re-rendering. */
  on: <T extends StreamEventName>(
    type: T,
    handler: (ev: StreamEvent) => void,
  ) => () => void;
}

export function useHarness(opts: UseHarnessOptions): UseHarnessState {
  const sessionRef = useRef<HarnessSession | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const [session, setSession] = useState<HarnessSession | null>(null);
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [lastEvent, setLastEvent] = useState<StreamEvent | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [done, setDone] = useState(false);

  const send = useCallback(
    (message?: string) => {
      const text = message ?? opts.initialMessage;
      if (!text) return;

      // Abort any prior session before starting a new one.
      abortRef.current?.abort();
      const ac = new AbortController();
      abortRef.current = ac;

      setEvents([]);
      setLastEvent(null);
      setError(null);
      setDone(false);

      const s = connect({
        ...opts,
        message: text,
        signal: ac.signal,
      });
      sessionRef.current = s;
      setSession(s);

      // Subscribe to every known event name so consumers get a unified
      // event log without enumerating types here.
      for (const name of StreamEventNames) {
        s.on(name, (ev) => {
          setEvents((prev) => [...prev, ev as StreamEvent]);
          setLastEvent(ev as StreamEvent);
        });
      }

      s.done()
        .then(() => setDone(true))
        .catch((err) =>
          setError(err instanceof Error ? err : new Error(String(err))),
        );
    },
    [opts],
  );

  // Auto-start once on mount if requested.
  const autoStartedRef = useRef(false);
  useEffect(() => {
    if (opts.autoStart && !autoStartedRef.current && opts.initialMessage) {
      autoStartedRef.current = true;
      send();
    }
    return () => {
      abortRef.current?.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const on = useCallback<UseHarnessState["on"]>((type, handler) => {
    const s = sessionRef.current;
    if (!s) return () => {};
    return s.on(type, handler as never);
  }, []);

  return { session, events, lastEvent, error, done, send, on };
}
