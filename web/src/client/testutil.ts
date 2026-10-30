import type { ConversationEvent } from "./types";

/** A Response whose body yields the given chunks, then ends (or stays open if `hold`). */
export function sseResponse(chunks: string[], opts: { status?: number; hold?: AbortSignal } = {}): Response {
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c));
      if (!opts.hold) return controller.close();
      opts.hold.addEventListener("abort", () => {
        try {
          controller.error(new DOMException("aborted", "AbortError"));
        } catch {
          /* already closed */
        }
      });
    },
  });
  return new Response(body, { status: opts.status ?? 200, headers: { "Content-Type": "text/event-stream" } });
}

/** A response factory whose body stays open until the request's own signal aborts, like a real fetch. */
export const heldSse =
  (chunks: string[]) =>
  (signal: AbortSignal | null | undefined): Response =>
    sseResponse(chunks, signal ? { hold: signal } : {});

export const frame = (e: ConversationEvent): string =>
  `id: ${e.seq}\nevent: ${e.kind}\ndata: ${JSON.stringify(e)}\n\n`;

export const ev = (seq: number, kind: ConversationEvent["kind"], extra: Partial<ConversationEvent> = {}): ConversationEvent => ({
  seq,
  kind,
  time: "2026-01-01T00:00:00Z",
  turnId: "t1",
  ...extra,
});

export const jsonResponse = (body: unknown, status = 200, headers: Record<string, string> = {}): Response =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

export interface FakeServerOptions {
  info?: Partial<Record<string, unknown>>;
  history?: ConversationEvent[];
  /** Status for POST /messages; default 202. */
  sendStatus?: number;
  /** Reject sends with this error envelope instead. */
  sendError?: { status: number; code: string; message: string };
}

/**
 * A fake conversation API behind a fetch function. `push` delivers an event on the open
 * stream, `drop` ends it (so the client reconnects), and the arrays record what was called.
 */
export function fakeServer(opts: FakeServerOptions = {}) {
  const enc = new TextEncoder();
  const calls: { method: string; url: string; headers: Headers; body?: unknown }[] = [];
  const streams: { lastEventId: string | null; signal: AbortSignal | null | undefined }[] = [];
  let controller: ReadableStreamDefaultController<Uint8Array> | null = null;
  const info = { id: "c1", title: "My chat", profile: "antigravity", resumable: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", ...opts.info };

  const fetchFn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const headers = new Headers(init?.headers);
    const body = typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : undefined;
    calls.push({ method, url, headers, ...(body !== undefined ? { body } : {}) });

    if (url.endsWith("/stream")) {
      streams.push({ lastEventId: headers.get("Last-Event-ID"), signal: init?.signal });
      const stream = new ReadableStream<Uint8Array>({
        start(c) {
          controller = c;
          for (const e of opts.history ?? []) c.enqueue(enc.encode(frame(e)));
          init?.signal?.addEventListener("abort", () => {
            try {
              c.error(new DOMException("aborted", "AbortError"));
            } catch {
              /* already closed */
            }
          });
        },
      });
      return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
    }
    if (method === "POST" && url.endsWith("/messages")) {
      if (opts.sendError) return jsonResponse({ error: { code: opts.sendError.code, message: opts.sendError.message } }, opts.sendError.status);
      return jsonResponse({ id: "t-new", status: "running", startedAt: "x" }, opts.sendStatus ?? 202);
    }
    if (method === "POST" && url.endsWith("/cancel")) return new Response(null, { status: 202 });
    if (url.includes("/export")) {
      return new Response("# exported", { headers: { "Content-Disposition": 'attachment; filename="conversation-c1.md"' } });
    }
    if (method === "POST" && url.endsWith("/conversations")) return jsonResponse({ ...info, id: "c-new", title: undefined }, 201);
    if (/\/conversations\?/.test(url) || url.endsWith("/conversations")) {
      // Page 2 (a cursor was sent) holds different conversations, as a real server's would.
      if (url.includes("cursor")) {
        return jsonResponse({ items: [{ ...info, id: "c3", title: "Third" }, { ...info, id: "c4", title: "Fourth" }] });
      }
      return jsonResponse({ items: [info, { ...info, id: "c2", title: "Second" }], nextCursor: "next" });
    }
    if (/\/conversations\/[^/?]+$/.test(url)) return jsonResponse(info);
    return new Response("not found", { status: 404 });
  }) as typeof fetch;

  return {
    fetch: fetchFn,
    calls,
    streams,
    push(e: ConversationEvent) {
      controller?.enqueue(enc.encode(frame(e)));
    },
    drop() {
      controller?.close();
    },
    callsTo: (suffix: string) => calls.filter((c) => c.url.endsWith(suffix) || c.url.includes(suffix)),
  };
}
