"use client";

import { useState, type FormEvent, type KeyboardEvent } from "react";
import { cls } from "./MessageList";

export interface MessageInputProps {
  onSend: (message: string) => void | Promise<void>;
  disabled?: boolean;
  /** Why sending is unavailable, shown under the box. */
  hint?: string;
  maxLength?: number;
  className?: string;
}

/** A message box. Enter inserts a line break; Ctrl/⌘+Enter or the button sends. */
export function MessageInput({ onSend, disabled = false, hint, maxLength = 8000, className }: MessageInputProps) {
  const [value, setValue] = useState("");
  const blank = value.trim() === "";

  async function submit(e?: FormEvent) {
    e?.preventDefault();
    if (disabled || blank) return;
    const text = value;
    setValue("");
    try {
      await onSend(text);
    } catch {
      setValue(text); // keep what the user typed if sending failed
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void submit();
  }

  return (
    <form className={cls("aicli-input", className)} onSubmit={submit}>
      <textarea
        aria-label="Message"
        value={value}
        maxLength={maxLength}
        disabled={disabled}
        rows={3}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onKeyDown}
      />
      <button type="submit" disabled={disabled || blank}>
        Send
      </button>
      {hint && <p className="aicli-hint">{hint}</p>}
    </form>
  );
}
