import { ApiError } from "./types.js";
// src/client/api.ts
/** Creates an API client. It never throws on construction. */
function createClient(options) {
	const base = options.baseUrl.replace(/\/+$/, "");
	const doFetch = options.fetch ?? ((input, init) => globalThis.fetch(input, init));
	async function request(path, init) {
		const extra = options.getHeaders ? await options.getHeaders() : {};
		const headers = new Headers(init.headers);
		for (const [k, v] of Object.entries(extra)) headers.set(k, v);
		const req = {
			...init,
			headers
		};
		if (options.credentials) req.credentials = options.credentials;
		return doFetch(base + path, req);
	}
	async function fail(res) {
		throw await readError(res);
	}
	async function json(path, init) {
		const res = await request(path, init);
		if (!res.ok) return fail(res);
		return {
			data: await res.json(),
			res
		};
	}
	const post = (body, signal) => ({
		method: "POST",
		headers: {
			"Content-Type": "application/json",
			Accept: "application/json"
		},
		body: JSON.stringify(body),
		...signal ? { signal } : {}
	});
	const get = (signal) => ({
		method: "GET",
		headers: { Accept: "application/json" },
		...signal ? { signal } : {}
	});
	const enc = encodeURIComponent;
	const query = (params) => {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(params)) if (v !== void 0) q.set(k, String(v));
		const s = q.toString();
		return s ? "?" + s : "";
	};
	return {
		options,
		request,
		conversations: {
			create: async (input, signal) => (await json("/conversations", post(input, signal))).data,
			list: async (input = {}, signal) => (await json("/conversations" + query({
				cursor: input.cursor,
				limit: input.limit
			}), get(signal))).data,
			get: async (id, signal) => (await json(`/conversations/${enc(id)}`, get(signal))).data
		},
		messages: { send: async (id, input, signal) => {
			const body = {
				message: input.message,
				clientMessageId: input.clientMessageId ?? newClientMessageId()
			};
			const { data, res } = await json(`/conversations/${enc(id)}/messages`, post(body, signal));
			return {
				turn: data,
				duplicate: res.status === 200
			};
		} },
		turns: { cancel: async (conversationId, turnId, signal) => {
			const res = await request(`/conversations/${enc(conversationId)}/turns/${enc(turnId)}/cancel`, {
				method: "POST",
				...signal ? { signal } : {}
			});
			if (!res.ok) await fail(res);
		} },
		events: { list: async (id, input = {}, signal) => (await json(`/conversations/${enc(id)}/events` + query({
			after: input.after,
			limit: input.limit
		}), get(signal))).data.items },
		exportFile: async (id, input, signal) => {
			const res = await request(`/conversations/${enc(id)}/export` + query({
				format: input.format,
				turnId: input.turnId
			}), {
				method: "GET",
				...signal ? { signal } : {}
			});
			if (!res.ok) return fail(res);
			const disposition = res.headers.get("Content-Disposition") ?? "";
			const filename = /filename="([^"]+)"/.exec(disposition)?.[1] ?? `conversation.${input.format}`;
			return {
				blob: await res.blob(),
				filename
			};
		}
	};
}
/** Turns an error response into an ApiError, using the server's error envelope when present. */
async function readError(res) {
	let code = "http_" + res.status;
	let message = res.statusText || "request failed";
	try {
		const body = await res.json();
		if (body.error?.code) code = body.error.code;
		if (body.error?.message) message = body.error.message;
	} catch {}
	return new ApiError(res.status, code, message);
}
/** A fresh idempotency key: letters, digits, "-" and "_", at most 64 characters. */
function newClientMessageId() {
	const c = globalThis.crypto;
	if (typeof c?.randomUUID === "function") return c.randomUUID();
	const bytes = /* @__PURE__ */ new Uint8Array(16);
	c.getRandomValues(bytes);
	return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}
export { createClient, newClientMessageId, readError };
