// Minimal SSE parser. Stays transport-only — no protocol knowledge.
//
// Conforms to the WHATWG EventSource format: each "event:" / "data:" /
// "id:" / "retry:" line accumulates until a blank line dispatches the
// event. Multi-line `data:` is concatenated with "\n" per spec.
//
// We do not depend on the browser EventSource because:
//  - it cannot send POST bodies (needed for chat input)
//  - it cannot send custom auth headers
//  - some servers stream over fetch + ReadableStream which works everywhere

export interface SSEEvent {
  event: string;
  data: string;
  id?: string;
  retry?: number;
}

/**
 * parseSSE consumes a Response body's text stream and yields parsed SSE
 * events. Caller is responsible for opening the request (with a custom
 * fetch, headers, body, etc.) and disposing the iterator.
 *
 * Usage:
 *
 *   const res = await fetch(url, { method: 'POST', body, signal });
 *   if (!res.body) throw new Error('no body');
 *   for await (const ev of parseSSE(res.body)) {
 *     // ev.event is "delta" | "interrupt_required" | ...
 *   }
 */
export async function* parseSSE(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<SSEEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8");
  let buffer = "";

  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      // Split on blank line ("\n\n" or "\r\n\r\n"). Anything before the
      // last separator is a complete event; the rest waits for more bytes.
      let sepIdx: number;
      while ((sepIdx = findSeparator(buffer)) !== -1) {
        const raw = buffer.slice(0, sepIdx);
        buffer = buffer.slice(sepIdx + (buffer[sepIdx] === "\r" ? 4 : 2));
        const ev = parseEventBlock(raw);
        if (ev) yield ev;
      }
    }
    // Flush any trailing event without a separator.
    if (buffer.trim().length > 0) {
      const ev = parseEventBlock(buffer);
      if (ev) yield ev;
    }
  } finally {
    reader.releaseLock();
  }
}

function findSeparator(s: string): number {
  const a = s.indexOf("\n\n");
  const b = s.indexOf("\r\n\r\n");
  if (a === -1) return b;
  if (b === -1) return a;
  return Math.min(a, b);
}

function parseEventBlock(raw: string): SSEEvent | null {
  const lines = raw.split(/\r?\n/);
  let event = "message";
  const data: string[] = [];
  let id: string | undefined;
  let retry: number | undefined;

  for (const line of lines) {
    if (line === "" || line.startsWith(":")) continue;
    const idx = line.indexOf(":");
    const field = idx === -1 ? line : line.slice(0, idx);
    let value = idx === -1 ? "" : line.slice(idx + 1);
    if (value.startsWith(" ")) value = value.slice(1);

    switch (field) {
      case "event":
        event = value;
        break;
      case "data":
        data.push(value);
        break;
      case "id":
        id = value;
        break;
      case "retry": {
        const n = Number(value);
        if (!Number.isNaN(n)) retry = n;
        break;
      }
    }
  }

  if (data.length === 0 && event === "message") return null;
  return { event, data: data.join("\n"), id, retry };
}
