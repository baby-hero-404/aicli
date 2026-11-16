// src/react/model.ts
/**
* Appends an event to an ordered list. An event whose seq is not above the last one is a
* duplicate (a reconnect replayed it) and leaves the list untouched, same reference.
*/
function addEvent(events, e) {
	const last = events[events.length - 1];
	if (last && e.seq <= last.seq) return events;
	return [...events, e];
}
/** Groups events into turns, in the order the turns started. Events without a turn are skipped. */
function buildTurns(events) {
	const turns = [];
	const byId = /* @__PURE__ */ new Map();
	for (const e of events) {
		if (!e.turnId) continue;
		let t = byId.get(e.turnId);
		if (!t) {
			t = {
				id: e.turnId,
				assistant: "",
				tools: [],
				status: "running"
			};
			byId.set(e.turnId, t);
			turns.push(t);
		}
		switch (e.kind) {
			case "user.message":
				t.user = e.text ?? "";
				break;
			case "assistant.text":
				t.assistant += e.text ?? "";
				break;
			case "tool.use":
				t.tools.push({
					name: e.tool?.name ?? "tool",
					...e.tool?.summary ? { summary: e.tool.summary } : {},
					done: false
				});
				break;
			case "tool.result": {
				const open = [...t.tools].reverse().find((x) => !x.done && x.name === (e.tool?.name ?? x.name));
				if (open) {
					open.done = true;
					if (e.tool?.summary) open.summary = e.tool.summary;
					if (e.tool?.isError) open.isError = true;
				}
				break;
			}
			case "turn.completed":
				t.status = "completed";
				break;
			case "turn.cancelled":
				t.status = "cancelled";
				break;
			case "turn.failed": {
				t.status = "failed";
				if (e.meta?.failure) t.failure = e.meta.failure;
				const secs = Number(e.meta?.retry_after_seconds);
				if (Number.isFinite(secs) && secs > 0) t.retryAfterSeconds = secs;
				break;
			}
		}
	}
	return turns;
}
/** The turn that is still running, if any. */
function runningTurn(turns) {
	return turns.find((t) => t.status === "running");
}
/** True when a turn failed because the CLI session is gone: the conversation can only be read. */
function sessionExpired(turns) {
	return turns.some((t) => t.failure === "session_expired");
}
/** A short, user-facing sentence for a failure code. */
function failureMessage(code, retryAfterSeconds) {
	switch (code) {
		case "quota_exceeded": return retryAfterSeconds ? `The AI usage quota is used up. It should be available again in about ${formatDuration(retryAfterSeconds)}.` : "The AI usage quota is used up.";
		case "auth_invalid": return "The AI account needs to sign in again.";
		case "awaiting_input": return "The assistant stopped to ask a question that cannot be answered here. Try rephrasing your request.";
		case "session_expired": return "This conversation can no longer be continued. You can still read and export it.";
		case "interrupted": return "The turn was interrupted. Send your message again.";
		default: return "The run failed.";
	}
}
function formatDuration(seconds) {
	if (seconds < 90) return `${Math.round(seconds)} seconds`;
	if (seconds < 5400) return `${Math.round(seconds / 60)} minutes`;
	return `${Math.round(seconds / 3600)} hours`;
}
export { addEvent, buildTurns, failureMessage, runningTurn, sessionExpired };
