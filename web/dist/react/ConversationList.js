"use client";
import { useClient } from "./AicliProvider.js";
import { cls } from "./MessageList.js";
import { describe } from "./Conversation.js";
import { useEffect, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";
// src/react/ConversationList.tsx
/** Your conversations, newest first, with a button to start a new one. */
function ConversationList({ profile, selectedId, onSelect, pageSize = 20, className }) {
	const client = useClient();
	const [items, setItems] = useState([]);
	const [next, setNext] = useState();
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState(null);
	useEffect(() => {
		const ac = new AbortController();
		client.conversations.list({ limit: pageSize }, ac.signal).then((page) => {
			setItems(page.items);
			setNext(page.nextCursor);
			setLoading(false);
		}, (err) => {
			if (ac.signal.aborted) return;
			setError(describe(err));
			setLoading(false);
		});
		return () => ac.abort();
	}, [client, pageSize]);
	async function loadMore() {
		if (!next) return;
		setLoading(true);
		try {
			const page = await client.conversations.list({
				cursor: next,
				limit: pageSize
			});
			setItems((prev) => [...prev, ...page.items]);
			setNext(page.nextCursor);
		} catch (err) {
			setError(describe(err));
		} finally {
			setLoading(false);
		}
	}
	async function create() {
		setError(null);
		try {
			const c = await client.conversations.create({ profile });
			setItems((prev) => [c, ...prev]);
			onSelect(c.id);
		} catch (err) {
			setError(describe(err));
		}
	}
	return /* @__PURE__ */ jsxs("nav", {
		className: cls("aicli-list", className),
		"aria-label": "Conversations",
		children: [
			/* @__PURE__ */ jsx("button", {
				type: "button",
				onClick: () => void create(),
				children: "New conversation"
			}),
			error && /* @__PURE__ */ jsx("p", {
				role: "alert",
				className: "aicli-error",
				children: error
			}),
			/* @__PURE__ */ jsx("ul", { children: items.map((c) => /* @__PURE__ */ jsx("li", { children: /* @__PURE__ */ jsxs("button", {
				type: "button",
				"aria-current": c.id === selectedId ? "true" : void 0,
				onClick: () => onSelect(c.id),
				children: [/* @__PURE__ */ jsx("span", {
					className: "aicli-list-title",
					children: c.title || "Untitled"
				}), /* @__PURE__ */ jsx("time", {
					dateTime: c.createdAt,
					children: new Date(c.createdAt).toLocaleString()
				})]
			}) }, c.id)) }),
			!loading && items.length === 0 && !error && /* @__PURE__ */ jsx("p", {
				className: "aicli-empty",
				children: "No conversations yet."
			}),
			next && /* @__PURE__ */ jsx("button", {
				type: "button",
				disabled: loading,
				onClick: () => void loadMore(),
				children: "Load more"
			})
		]
	});
}
export { ConversationList };
