"use client";
import { failureMessage } from "./model.js";
import { jsx, jsxs } from "react/jsx-runtime";
// src/react/MessageList.tsx
const pre = {
	whiteSpace: "pre-wrap",
	overflowWrap: "anywhere"
};
/**
* The transcript. Everything is rendered as text: output from a CLI is untrusted, so it is
* never interpreted as HTML or markdown.
*/
function MessageList({ turns, className }) {
	return /* @__PURE__ */ jsx("div", {
		role: "log",
		"aria-live": "polite",
		"aria-relevant": "additions text",
		"aria-label": "Conversation",
		className: cls("aicli-messages", className),
		children: turns.map((t) => /* @__PURE__ */ jsxs("article", {
			className: "aicli-turn",
			"data-status": t.status,
			children: [
				t.user !== void 0 && /* @__PURE__ */ jsxs("div", {
					className: "aicli-user",
					children: [/* @__PURE__ */ jsx("span", {
						className: "aicli-author",
						children: "You"
					}), /* @__PURE__ */ jsx("p", {
						style: pre,
						children: t.user
					})]
				}),
				t.tools.length > 0 && /* @__PURE__ */ jsx("ul", {
					className: "aicli-tools",
					children: t.tools.map((tool, i) => /* @__PURE__ */ jsxs("li", {
						className: "aicli-tool",
						"data-done": tool.done,
						"data-error": tool.isError ? "true" : void 0,
						children: [/* @__PURE__ */ jsx("span", {
							className: "aicli-tool-name",
							children: tool.name
						}), tool.summary && /* @__PURE__ */ jsx("span", {
							className: "aicli-tool-summary",
							style: pre,
							children: ` ${tool.summary}`
						})]
					}, i))
				}),
				(t.assistant !== "" || t.status === "running") && /* @__PURE__ */ jsxs("div", {
					className: "aicli-assistant",
					children: [/* @__PURE__ */ jsx("span", {
						className: "aicli-author",
						children: "Assistant"
					}), /* @__PURE__ */ jsx("p", {
						style: pre,
						children: t.assistant
					})]
				}),
				t.status === "failed" && /* @__PURE__ */ jsx("p", {
					role: "alert",
					className: "aicli-failure",
					children: failureMessage(t.failure, t.retryAfterSeconds)
				}),
				t.status === "cancelled" && /* @__PURE__ */ jsx("p", {
					className: "aicli-cancelled",
					children: "Cancelled."
				})
			]
		}, t.id))
	});
}
function cls(...names) {
	return names.filter(Boolean).join(" ");
}
export { MessageList, cls };
