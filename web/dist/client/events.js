import { ApiError } from "./types.js";
import { readError } from "./api.js";
import { SseParser } from "./sse.js";
// src/client/events.ts
/** Statuses that retrying cannot fix: the stream stops and the promise rejects. */
const FATAL = /* @__PURE__ */ new Set([
	400,
	401,
	403,
	404
]);
/**
* Follows a conversation's event stream until the signal aborts. One call serves a whole
* conversation: it stays connected across turns, reconnects after a drop, and resumes from the
* last event it delivered, so every event is passed to `onEvent` exactly once and in order.
*
* Rejects with an ApiError on a status that retrying cannot fix (401, 403, 404, 400).
*/
async function streamEvents(client, id, opts) {
	const { signal } = opts;
	const initial = opts.backoff?.initialMs ?? 500;
	const max = opts.backoff?.maxMs ?? 3e4;
	let last = opts.lastEventId ?? 0;
	let attempt = 0;
	let first = true;
	try {
		while (!signal.aborted) {
			opts.onStatus?.(first ? "connecting" : "reconnecting");
			first = false;
			try {
				const res = await client.request(`/conversations/${encodeURIComponent(id)}/stream`, {
					method: "GET",
					headers: {
						Accept: "text/event-stream",
						"Last-Event-ID": String(last)
					},
					signal
				});
				if (!res.ok) throw await readError(res);
				if (!res.body) throw new Error("the response has no body");
				opts.onStatus?.("open");
				attempt = 0;
				last = await pump(res.body, last, opts.onEvent);
			} catch (err) {
				if (signal.aborted) break;
				if (err instanceof ApiError && FATAL.has(err.status)) throw err;
				opts.onRetry?.(err, attempt);
			}
			if (signal.aborted) break;
			await sleep(delay(initial, max, attempt), signal);
			attempt++;
		}
	} finally {
		opts.onStatus?.("closed");
	}
}
/** Reads the body to its end and returns the highest seq delivered. */
async function pump(body, last, onEvent) {
	const reader = body.getReader();
	const decoder = new TextDecoder();
	const parser = new SseParser();
	try {
		for (;;) {
			const { done, value } = await reader.read();
			if (done) return last;
			for (const msg of parser.feed(decoder.decode(value, { stream: true }))) {
				const ev = parseEvent(msg.data);
				if (!ev || ev.seq <= last) continue;
				last = ev.seq;
				onEvent(ev);
			}
		}
	} finally {
		reader.releaseLock();
	}
}
function parseEvent(data) {
	try {
		const v = JSON.parse(data);
		if (typeof v.seq !== "number" || typeof v.kind !== "string") return void 0;
		return v;
	} catch {
		return;
	}
}
function delay(initial, max, attempt) {
	const ceiling = Math.min(max, initial * 2 ** Math.min(attempt, 30));
	return ceiling / 2 + Math.random() * (ceiling / 2);
}
function sleep(ms, signal) {
	return new Promise((resolve) => {
		if (signal.aborted) return resolve();
		const done = () => {
			clearTimeout(timer);
			signal.removeEventListener("abort", done);
			resolve();
		};
		const timer = setTimeout(done, ms);
		signal.addEventListener("abort", done, { once: true });
	});
}
export { streamEvents };
