// useArtifacts — collects file/component artifacts emitted by the stream.

import type { Artifact, HarnessSession, StreamEvent } from "@harness/client";
import { useEffect, useState } from "react";

export function useArtifacts(session: HarnessSession | null): Artifact[] {
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);

  useEffect(() => {
    if (!session) return;

    const upsert = (a: Artifact) => {
      setArtifacts((prev) => {
        const idx = prev.findIndex((x) => x.id === a.id);
        if (idx === -1) return [...prev, a];
        const next = prev.slice();
        next[idx] = a;
        return next;
      });
    };

    const offCreated = session.on("artifact_created", (ev: StreamEvent) => {
      if (ev.artifact) upsert(ev.artifact as Artifact);
    });
    const offUpdated = session.on("artifact_updated", (ev: StreamEvent) => {
      if (ev.artifact) upsert(ev.artifact as Artifact);
    });

    return () => {
      offCreated();
      offUpdated();
    };
  }, [session]);

  return artifacts;
}
