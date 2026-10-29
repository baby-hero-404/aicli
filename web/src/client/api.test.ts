// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { createClient, newClientMessageId } from "./api";
import { ApiError } from "./types";
import { jsonResponse } from "./testutil";

type FetchFn = typeof fetch;
const conv = { id: "c1", profile: "antigravity", resumable: true, createdAt: "x", updatedAt: "x" };

function setup(handler: (url: string, init: RequestInit) => Response | Promise<Response>, extra = {}) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchFn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    return handler(String(input), init ?? {});
  }) as unknown as FetchFn;
  return { client: createClient({ baseUrl: "/api/aicli/", fetch: fetchFn, ...extra }), calls };
}

describe("createClient", () => {
  it("sends the host's headers and credentials mode on every request, and never puts them in the URL", async () => {
    const { client, calls } = setup(() => jsonResponse(conv), {
      getHeaders: async () => ({ Authorization: "Bearer secret-token" }),
      credentials: "include",
    });
    await client.conversations.get("c1");
    await client.conversations.create({ profile: "antigravity" });
    for (const { url, init } of calls) {
      expect(new Headers(init.headers).get("Authorization")).toBe("Bearer secret-token");
      expect(init.credentials).toBe("include");
      expect(url).not.toContain("secret-token");
    }
  });

  it("calls getHeaders per request so tokens can be refreshed", async () => {
    let n = 0;
    const { client, calls } = setup(() => jsonResponse(conv), { getHeaders: () => ({ "X-N": String(++n) }) });
    await client.conversations.get("a");
    await client.conversations.get("b");
    expect(calls.map((c) => new Headers(c.init.headers).get("X-N"))).toEqual(["1", "2"]);
  });

  it("builds URLs from the base without a double slash and encodes ids", async () => {
    const { client, calls } = setup(() => jsonResponse(conv));
    await client.conversations.get("a/b c");
    expect(calls[0]?.url).toBe("/api/aicli/conversations/a%2Fb%20c");
  });

  it("posts JSON for create and send", async () => {
    const { client, calls } = setup(() => jsonResponse({ id: "t1", status: "running", startedAt: "x" }, 202));
    await client.messages.send("c1", { message: "hi", clientMessageId: "m1" });
    const { init } = calls[0]!;
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(String(init.body))).toEqual({ message: "hi", clientMessageId: "m1" });
  });

  it("generates a valid clientMessageId when none is given, and reports duplicates", async () => {
    let status = 202;
    const { client, calls } = setup(() => jsonResponse({ id: "t1", status: "running", startedAt: "x" }, status));
    const first = await client.messages.send("c1", { message: "hi" });
    expect(first.duplicate).toBe(false);
    expect(JSON.parse(String(calls[0]!.init.body)).clientMessageId).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    status = 200;
    expect((await client.messages.send("c1", { message: "hi", clientMessageId: "same" })).duplicate).toBe(true);
  });

  it("maps the server's error envelope to ApiError", async () => {
    const { client } = setup(() =>
      jsonResponse({ error: { code: "turn_in_progress", message: "a turn is already running" } }, 409),
    );
    const err = await client.messages.send("c1", { message: "hi" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 409, code: "turn_in_progress", message: "a turn is already running" });
  });

  it("copes with an error response that is not JSON", async () => {
    const { client } = setup(() => new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" }));
    await expect(client.conversations.get("c1")).rejects.toMatchObject({ status: 502, code: "http_502" });
  });

  it("lists with query parameters and cancels with a POST", async () => {
    const { client, calls } = setup((url) => (url.includes("/cancel") ? new Response(null, { status: 202 }) : jsonResponse({ items: [] })));
    await client.conversations.list({ cursor: "abc", limit: 5 });
    await client.events.list("c1", { after: 3, limit: 10 });
    await client.turns.cancel("c1", "t1");
    expect(calls[0]?.url).toBe("/api/aicli/conversations?cursor=abc&limit=5");
    expect(calls[1]?.url).toBe("/api/aicli/conversations/c1/events?after=3&limit=10");
    expect(calls[2]?.url).toBe("/api/aicli/conversations/c1/turns/t1/cancel");
    expect(calls[2]?.init.method).toBe("POST");
  });

  it("downloads an export through fetch and reads the file name", async () => {
    const { client, calls } = setup(
      () => new Response("# hi", { headers: { "Content-Disposition": 'attachment; filename="conversation-c1.md"' } }),
      { getHeaders: () => ({ Authorization: "Bearer t" }) },
    );
    const file = await client.exportFile("c1", { format: "md", turnId: "t1" });
    expect(file.filename).toBe("conversation-c1.md");
    expect(await file.blob.text()).toBe("# hi");
    expect(calls[0]?.url).toBe("/api/aicli/conversations/c1/export?format=md&turnId=t1");
    expect(new Headers(calls[0]!.init.headers).get("Authorization")).toBe("Bearer t");
  });
});

describe("newClientMessageId", () => {
  it("is unique and matches the server's pattern", () => {
    const ids = new Set(Array.from({ length: 200 }, newClientMessageId));
    expect(ids.size).toBe(200);
    for (const id of ids) expect(id).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
  });
});
