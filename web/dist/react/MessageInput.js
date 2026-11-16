"use client";
import { cls } from "./MessageList.js";
import { useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";
// src/react/MessageInput.tsx
/** A message box. Enter inserts a line break; Ctrl/⌘+Enter or the button sends. */
function MessageInput({ onSend, disabled = false, hint, maxLength = 8e3, className }) {
	const [value, setValue] = useState("");
	const blank = value.trim() === "";
	async function submit(e) {
		e?.preventDefault();
		if (disabled || blank) return;
		const text = value;
		setValue("");
		try {
			await onSend(text);
		} catch {
			setValue(text);
		}
	}
	function onKeyDown(e) {
		if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) submit();
	}
	return /* @__PURE__ */ jsxs("form", {
		className: cls("aicli-input", className),
		onSubmit: submit,
		children: [
			/* @__PURE__ */ jsx("textarea", {
				"aria-label": "Message",
				value,
				maxLength,
				disabled,
				rows: 3,
				onChange: (e) => setValue(e.target.value),
				onKeyDown
			}),
			/* @__PURE__ */ jsx("button", {
				type: "submit",
				disabled: disabled || blank,
				children: "Send"
			}),
			hint && /* @__PURE__ */ jsx("p", {
				className: "aicli-hint",
				children: hint
			})
		]
	});
}
export { MessageInput };
