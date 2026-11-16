"use client";
import { ApiError } from "../client/types.js";
import { streamEvents } from "../client/events.js";
import { useClient } from "./AicliProvider.js";
import { addEvent, buildTurns, runningTurn, sessionExpired } from "./model.js";
import { MessageList, cls } from "./MessageList.js";
import { ExportMenu, saveBlob } from "./ExportMenu.js";
import { MessageInput } from "./MessageInput.js";
import { TurnStatus } from "./TurnStatus.js";
import { useEffect, useMemo, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";
// src/react/Conversation.tsx
/**
* A chat view for one conversation: history, live output, sending, cancelling and exporting.
* It opens a single event stream when mounted and keeps it across turns; switching
* `conversationId` starts over with the new conversation.
*/
function Conversation({ conversationId, className }) {
	return /* @__PURE__ */ jsx(ConversationView, {
		conversationId,
		...className ? { className } : {}
	}, conversationId);
}
function ConversationView({ conversationId, className }) {
	const client = useClient();
	const [events, setEvents] = useState([]);
	const [connection, setConnection] = useState("connecting");
	const [info, setInfo] = useState(null);
	const [error, setError] = useState(null);
	const [pendingTurn, setPendingTurn] = useState(null);
	const [cancellingTurn, setCancellingTurn] = useState(null);
	useEffect(() => {
		const ac = new AbortController();
		const report = (err) => {
			if (!ac.signal.aborted) setError(describe(err));
		};
		client.conversations.get(conversationId, ac.signal).then(setInfo, report);
		streamEvents(client, conversationId, {
			signal: ac.signal,
			onEvent: (e) => setEvents((prev) => addEvent(prev, e)),
			onStatus: setConnection
		}).catch(report);
		return () => ac.abort();
	}, [client, conversationId]);
	const turns = useMemo(() => buildTurns(events), [events]);
	const running = runningTurn(turns);
	const awaitingFirstEvent = pendingTurn !== null && !turns.some((t) => t.id === pendingTurn);
	const working = running !== void 0 || awaitingFirstEvent;
	const expired = sessionExpired(turns) || info?.resumable === false;
	const canSend = connection === "open" && !working && !expired;
	async function send(message) {
		setError(null);
		try {
			const { turn } = await client.messages.send(conversationId, { message });
			setPendingTurn(turn.id);
		} catch (err) {
			setError(describe(err));
			throw err;
		}
	}
	function cancel() {
		const id = running?.id ?? pendingTurn;
		if (!id) return;
		setCancellingTurn(id);
		client.turns.cancel(conversationId, id).catch((err) => {
			setCancellingTurn(null);
			setError(describe(err));
		});
	}
	async function exportAs(format) {
		const file = await client.exportFile(conversationId, { format });
		saveBlob(file.blob, file.filename);
	}
	let hint;
	if (expired) hint = "This conversation can no longer be continued.";
	else if (connection !== "open") hint = "Waiting for the connection…";
	const activeTurn = running?.id ?? pendingTurn;
	return /* @__PURE__ */ jsxs("section", {
		className: cls("aicli-conversation", className),
		"aria-label": info?.title || "Conversation",
		children: [
			/* @__PURE__ */ jsx("div", {
				className: "aicli-toolbar",
				children: /* @__PURE__ */ jsx(ExportMenu, {
					onExport: exportAs,
					disabled: turns.length === 0
				})
			}),
			/* @__PURE__ */ jsx(MessageList, { turns }),
			/* @__PURE__ */ jsx(TurnStatus, {
				connection,
				working,
				cancelling: cancellingTurn !== null && cancellingTurn === activeTurn,
				onCancel: cancel
			}),
			error && /* @__PURE__ */ jsx("p", {
				role: "alert",
				className: "aicli-error",
				children: error
			}),
			/* @__PURE__ */ jsx(MessageInput, {
				onSend: send,
				disabled: !canSend,
				...hint ? { hint } : {}
			})
		]
	});
}
/** A user-facing sentence for an error; details of unexpected failures are not shown. */
function describe(err) {
	if (err instanceof ApiError) switch (err.code) {
		case "turn_in_progress": return "A message is already being processed. Wait for it to finish.";
		case "rate_limited": return "You are sending too fast. Wait a moment and try again.";
		case "not_resumable": return "This conversation can no longer be continued.";
		case "message_too_large": return "That message is too long.";
		case "unauthenticated": return "You are signed out. Sign in again to continue.";
		case "not_found":
		case "forbidden": return "This conversation does not exist, or you do not have access to it.";
		case "turn_not_running": return "The turn had already finished.";
		default: return err.status >= 500 ? "The server had a problem. Try again in a moment." : err.message;
	}
	return "Something went wrong. Check your connection and try again.";
}
export { Conversation, describe };
