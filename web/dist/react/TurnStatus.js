"use client";
import { cls } from "./MessageList.js";
import { jsx, jsxs } from "react/jsx-runtime";
// src/react/TurnStatus.tsx
/** Connection and progress line, with the Cancel button while a turn runs. */
function TurnStatus({ connection, working, cancelling, onCancel, className }) {
	let text = "";
	if (connection === "connecting") text = "Connecting…";
	else if (connection === "reconnecting") text = "Connection lost. Reconnecting…";
	else if (cancelling) text = "Cancelling…";
	else if (working) text = "Working…";
	return /* @__PURE__ */ jsxs("div", {
		role: "status",
		className: cls("aicli-status", className),
		"data-connection": connection,
		children: [/* @__PURE__ */ jsx("span", { children: text }), working && !cancelling && /* @__PURE__ */ jsx("button", {
			type: "button",
			onClick: onCancel,
			children: "Cancel"
		})]
	});
}
export { TurnStatus };
