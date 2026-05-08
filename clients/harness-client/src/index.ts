// @harness/client — public entrypoint.
//
// The generated/ folder mirrors sdk/*.go via tygo: every type and constant
// the wire protocol exposes lives there. The hand-written transport.ts and
// session.ts depend only on those types, so adding a new field or event in
// Go is automatically picked up after running `make client-types`.

export * from "./generated/index.js";
export {
  connect,
  type ConnectOptions,
  type HarnessSession,
} from "./session.js";
export { parseSSE, type SSEEvent } from "./transport.js";
