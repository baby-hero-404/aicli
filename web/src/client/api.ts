import {
  ApiError,
  type ClientOptions,
  type Conversation,
  type ConversationEvent,
  type ConversationPage,
  type ExportFormat,
  type Turn,
} from "./types";

export interface SendResult {
  turn: Turn;
  /** True when the server had already seen this clientMessageId and started nothing new. */
  duplicate: boolean;
}

export interface ExportedFile {
  blob: Blob;
  /** Suggested file name from the server. */
  filename: string;
}

export interface Client {
  readonly options: Readonly<ClientOptions>;
  conversations: {
    create(input: { profile: string; title?: string }, signal?: AbortSignal): Promise<Conversation>;
    list(input?: { cursor?: string; limit?: number }, signal?: AbortSignal): Promise<ConversationPage>;
    get(id: string, signal?: AbortSignal): Promise<Conversation>;
  };
  messages: {
    /** `clientMessageId` makes a retry safe; one is generated when omitted. */
    send(
      id: string,
      input: { message: string; clientMessageId?: string },
      signal?: AbortSignal,
    ): Promise<SendResult>;
  };
  turns: {
    /** Resolves when the request was accepted; the outcome arrives on the event stream. */
    cancel(conversationId: string, turnId: string, signal?: AbortSignal): Promise<void>;
  };
  events: {
    list(
      id: string,
      input?: { after?: number; limit?: number },
      signal?: AbortSignal,
    ): Promise<ConversationEvent[]>;
  };
  /** Downloads an export through fetch, so the host's auth header is sent. */
  exportFile(
    id: string,
    input: { format: ExportFormat; turnId?: string },
    signal?: AbortSignal,
  ): Promise<ExportedFile>;
  /** @internal Authenticated fetch used by the event stream. */
  request(path: string, init: RequestInit): Promise<Response>;
}

/** Creates an API client. It never throws on construction. */
export function createClient(options: ClientOptions): Client {
  const base = options.baseUrl.replace(/\/+$/, "");
  const doFetch = options.fetch ?? ((input, init) => globalThis.fetch(input, init));

  async function request(path: string, init: RequestInit): Promise<Response> {
    const extra = options.getHeaders ? await options.getHeaders() : {};
    const headers = new Headers(init.headers);
    for (const [k, v] of Object.entries(extra)) headers.set(k, v);
    const req: RequestInit = { ...init, headers };
    if (options.credentials) req.credentials = options.credentials;
    return doFetch(base + path, req);
  }

  async function fail(res: Response): Promise<never> {
    throw await readError(res);
  }

  async function json<T>(path: string, init: RequestInit): Promise<{ data: T; res: Response }> {
    const res = await request(path, init);
    if (!res.ok) return fail(res);
    return { data: (await res.json()) as T, res };
  }

  const post = (body: unknown, signal?: AbortSignal): RequestInit => ({
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
    ...(signal ? { signal } : {}),
  });
  const get = (signal?: AbortSignal): RequestInit => ({
    method: "GET",
    headers: { Accept: "application/json" },
    ...(signal ? { signal } : {}),
  });
  const enc = encodeURIComponent;
  const query = (params: Record<string, string | number | undefined>): string => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) if (v !== undefined) q.set(k, String(v));
    const s = q.toString();
    return s ? "?" + s : "";
  };

  return {
    options,
    request,
    conversations: {
      create: async (input, signal) => (await json<Conversation>("/conversations", post(input, signal))).data,
      list: async (input = {}, signal) =>
        (await json<ConversationPage>("/conversations" + query({ cursor: input.cursor, limit: input.limit }), get(signal))).data,
      get: async (id, signal) => (await json<Conversation>(`/conversations/${enc(id)}`, get(signal))).data,
    },
    messages: {
      send: async (id, input, signal) => {
        const body = { message: input.message, clientMessageId: input.clientMessageId ?? newClientMessageId() };
        const { data, res } = await json<Turn>(`/conversations/${enc(id)}/messages`, post(body, signal));
        return { turn: data, duplicate: res.status === 200 };
      },
    },
    turns: {
      cancel: async (conversationId, turnId, signal) => {
        const res = await request(`/conversations/${enc(conversationId)}/turns/${enc(turnId)}/cancel`, {
          method: "POST",
          ...(signal ? { signal } : {}),
        });
        if (!res.ok) await fail(res);
      },
    },
    events: {
      list: async (id, input = {}, signal) =>
        (
          await json<{ items: ConversationEvent[] }>(
            `/conversations/${enc(id)}/events` + query({ after: input.after, limit: input.limit }),
            get(signal),
          )
        ).data.items,
    },
    exportFile: async (id, input, signal) => {
      const res = await request(
        `/conversations/${enc(id)}/export` + query({ format: input.format, turnId: input.turnId }),
        { method: "GET", ...(signal ? { signal } : {}) },
      );
      if (!res.ok) return fail(res);
      const disposition = res.headers.get("Content-Disposition") ?? "";
      const filename = /filename="([^"]+)"/.exec(disposition)?.[1] ?? `conversation.${input.format}`;
      return { blob: await res.blob(), filename };
    },
  };
}

/** Turns an error response into an ApiError, using the server's error envelope when present. */
export async function readError(res: Response): Promise<ApiError> {
  let code = "http_" + res.status;
  let message = res.statusText || "request failed";
  try {
    const body = (await res.json()) as { error?: { code?: string; message?: string } };
    if (body.error?.code) code = body.error.code;
    if (body.error?.message) message = body.error.message;
  } catch {
    /* not JSON: keep the defaults */
  }
  return new ApiError(res.status, code, message);
}

/** A fresh idempotency key: letters, digits, "-" and "_", at most 64 characters. */
export function newClientMessageId(): string {
  const c = globalThis.crypto;
  if (typeof c?.randomUUID === "function") return c.randomUUID();
  const bytes = new Uint8Array(16);
  c.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}
