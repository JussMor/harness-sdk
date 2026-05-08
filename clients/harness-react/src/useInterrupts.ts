// useInterrupts — collects pending interrupts (approval / question / form_input)
// and exposes a unified resolve() callback. Built on top of a HarnessSession
// returned from useHarness.

import type {
  HarnessSession,
  InterruptRequest,
  StreamEvent,
} from "@harness/client";
import { useCallback, useEffect, useState } from "react";

export interface PendingInterrupt {
  request: InterruptRequest;
  receivedAt: number;
}

export interface UseInterruptsResult {
  pending: PendingInterrupt[];
  resolve: (
    chatId: string | number,
    id: string,
    response: { approved?: boolean; answer?: unknown; modifiedArgs?: string },
  ) => Promise<void>;
}

export function useInterrupts(
  session: HarnessSession | null,
): UseInterruptsResult {
  const [pending, setPending] = useState<PendingInterrupt[]>([]);

  useEffect(() => {
    if (!session) return;

    const offRequired = session.on("interrupt_required", (ev: StreamEvent) => {
      if (!ev.interrupt) return;
      setPending((prev) => [
        ...prev,
        { request: ev.interrupt as InterruptRequest, receivedAt: Date.now() },
      ]);
    });

    const offResolved = session.on("interrupt_resolved", (ev: StreamEvent) => {
      if (!ev.interrupt) return;
      const id = (ev.interrupt as InterruptRequest).id;
      setPending((prev) => prev.filter((p) => p.request.id !== id));
    });

    // Legacy approval events keep older backends working. ApprovalRequest
    // has no `json:` tags in Go so tygo emits PascalCase fields.
    const offLegacy = session.on("confirmation_required", (ev: StreamEvent) => {
      const legacy = ev.confirmation_request;
      if (!legacy) return;
      setPending((prev) =>
        prev.some((p) => p.request.id === legacy.ID)
          ? prev
          : [
              ...prev,
              {
                request: {
                  id: legacy.ID,
                  kind: "approval",
                  reason: legacy.Reason,
                  created_at: legacy.CreatedAt,
                  approval: { tool_call: legacy.ToolCall },
                } as InterruptRequest,
                receivedAt: Date.now(),
              },
            ],
      );
    });

    return () => {
      offRequired();
      offResolved();
      offLegacy();
    };
  }, [session]);

  const resolve = useCallback<UseInterruptsResult["resolve"]>(
    async (chatId, id, response) => {
      if (!session) throw new Error("useInterrupts.resolve: no active session");
      await session.resolveInterrupt(chatId, id, response);
      setPending((prev) => prev.filter((p) => p.request.id !== id));
    },
    [session],
  );

  return { pending, resolve };
}
