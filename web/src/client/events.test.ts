// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { createClient } from "./api";
import { streamEvents, type StreamStatus } from "./events";
import { ApiError, type ConversationEvent } from "./types";
import { ev, frame, heldSse, jsonResponse, sseResponse } from "./testutil";

type FetchFn = typeof fetch;
const fast = { backoff: { initialMs: 1, maxMs: 2 } };

type Reply = Response | Error | ((signal: AbortSignal | null | undefined) => Response);

function run(responses: Reply[], extra: Partial<Parameters<typeof streamEvents>[2]> = {}) {
  const sent: { lastEventId: string | null; accept: string | null }[] = [];
  const ac = new AbortController();
  let i = 0;
  const fetchFn = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
    const h = new Headers(init?.headers);
    sent.push({ lastEventId: h.get("Last-Event-ID"), accept: h.get("Accept") });
    const r = responses[Math.min(i++, responses.length - 1)]!;
    if (r instanceof Error) throw r;
    return typeof r === "function" ? r(init?.signal) : r;
  }) as unknown as FetchFn;
  const client = createClient({ baseUrl: "/api", fetch: fetchFn });
  const events: ConversationEvent[] = [];
  const statuses: StreamStatus[] = [];
  const done = streamEvents(client, "c1", {
    signal: ac.signal,
    onEvent: (e) => events.push(e),
    onStatus: (s) => statuses.push(s),
    ...fast,
    ...extra,
  });
  return { ac, sent, events, statuses, done, calls: () => i };
}

const until = async (cond: () => boolean) => {
  for (let i = 0; i < 400 && !cond(); i++) await new Promise((r) => setTimeout(r, 5));
  expect(cond()).toBe(true);
};

describe("streamEvents", () => {
  it("delivers events in order and asks for text/event-stream with Last-Event-ID 0", async () => {
    const r = run([heldSse([frame(ev(1, "user.message")), frame(ev(2, "turn.started"))])]);
    await until(() => r.events.length === 2);
    r.ac.abort();
    await r.done;
    expect(r.events.map((e) => e.seq)).toEqual([1, 2]);
    expect(r.sent[0]).toEqual({ lastEventId: "0", accept: "text/event-stream" });
    expect(r.statuses).toEqual(["connecting", "open", "closed"]);
  });

  it("reconnects after the stream ends and resumes from the last seq, with no duplicate", async () => {
    // The second connection replays seq 2 again (a server may), then continues.
    const r = run([
      sseResponse([frame(ev(1, "user.message")), frame(ev(2, "assistant.text", { text: "a" }))]),
      heldSse([frame(ev(2, "assistant.text", { text: "a" })), frame(ev(3, "turn.completed"))]),
    ]);
    await until(() => r.events.length === 3);
    r.ac.abort();
    await r.done;
    expect(r.events.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(r.sent.map((s) => s.lastEventId)).toEqual(["0", "2"]);
    expect(r.statuses).toContain("reconnecting");
  });

  it("starts from lastEventId when given", async () => {
    const r = run([heldSse([])], { lastEventId: 41 });
    await until(() => r.calls() === 1);
    r.ac.abort();
    await r.done;
    expect(r.sent[0]?.lastEventId).toBe("41");
  });

  it("retries after a network error and a 503, reporting each", async () => {
    const retries: unknown[] = [];
    const r = run(
      [new TypeError("network down"), jsonResponse({ error: { code: "internal", message: "x" } }, 503), heldSse([frame(ev(1, "user.message"))])],
      { onRetry: (e) => retries.push(e) },
    );
    await until(() => r.events.length === 1);
    r.ac.abort();
    await r.done;
    expect(retries).toHaveLength(2);
    expect(retries[0]).toBeInstanceOf(TypeError);
    expect(retries[1]).toMatchObject({ status: 503 });
  });

  for (const status of [400, 401, 403, 404]) {
    it(`stops and rejects on ${status}`, async () => {
      const r = run([jsonResponse({ error: { code: "x", message: "no" } }, status)]);
      await expect(r.done).rejects.toMatchObject({ status });
      await expect(r.done).rejects.toBeInstanceOf(ApiError);
      expect(r.calls()).toBe(1);
      expect(r.statuses.at(-1)).toBe("closed");
    });
  }

  it("ignores malformed frames and events it already has", async () => {
    const r = run([
      heldSse([
        "data: not json\n\n",
        'data: {"seq":"x"}\n\n',
        frame(ev(1, "user.message")),
        frame(ev(1, "user.message")),
        ": ping\n\n",
        frame(ev(2, "turn.started")),
      ]),
    ]);
    await until(() => r.events.length === 2);
    r.ac.abort();
    await r.done;
    expect(r.events.map((e) => e.seq)).toEqual([1, 2]);
  });

  it("decodes a multi-byte character split across chunks", async () => {
    const bytes = new TextEncoder().encode(frame(ev(1, "assistant.text", { text: "héllo wörld" })));
    const cut = bytes.indexOf(0xc3) + 1; // between the two bytes of "é"
    const ac = new AbortController();
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(bytes.slice(0, cut));
        c.enqueue(bytes.slice(cut));
        ac.signal.addEventListener("abort", () => c.error(new DOMException("aborted", "AbortError")));
      },
    });
    const r = run([new Response(body, { status: 200 })], { signal: ac.signal });
    await until(() => r.events.length === 1);
    ac.abort();
    await r.done;
    expect(r.events[0]?.text).toBe("héllo wörld");
  });

  it("stops promptly when aborted while waiting to reconnect", async () => {
    const r = run([new TypeError("down")], { backoff: { initialMs: 60_000, maxMs: 60_000 } });
    await until(() => r.calls() === 1);
    r.ac.abort();
    await r.done; // would hang for a minute if the sleep ignored the signal
    expect(r.statuses.at(-1)).toBe("closed");
  });
});
