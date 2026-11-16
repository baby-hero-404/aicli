"use client";
import { cls } from "./MessageList.js";
import { useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";
// src/react/ExportMenu.tsx
/** Export buttons. Each runs onExport, which downloads through the authenticated client. */
function ExportMenu({ onExport, disabled = false, className }) {
	const [busy, setBusy] = useState(null);
	const [failed, setFailed] = useState(false);
	async function run(format) {
		setBusy(format);
		setFailed(false);
		try {
			await onExport(format);
		} catch {
			setFailed(true);
		} finally {
			setBusy(null);
		}
	}
	return /* @__PURE__ */ jsxs("div", {
		className: cls("aicli-export", className),
		children: [
			/* @__PURE__ */ jsx("button", {
				type: "button",
				disabled: disabled || busy !== null,
				onClick: () => void run("md"),
				children: "Export Markdown"
			}),
			/* @__PURE__ */ jsx("button", {
				type: "button",
				disabled: disabled || busy !== null,
				onClick: () => void run("json"),
				children: "Export JSON"
			}),
			failed && /* @__PURE__ */ jsx("span", {
				role: "alert",
				className: "aicli-error",
				children: "Export failed."
			})
		]
	});
}
/** Saves a blob through a temporary object URL. The URL is revoked right after the click. */
function saveBlob(blob, filename) {
	const url = URL.createObjectURL(blob);
	try {
		const a = document.createElement("a");
		a.href = url;
		a.download = filename;
		a.rel = "noopener";
		a.style.display = "none";
		document.body.appendChild(a);
		a.click();
		a.remove();
	} finally {
		setTimeout(() => URL.revokeObjectURL(url), 0);
	}
}
export { ExportMenu, saveBlob };
