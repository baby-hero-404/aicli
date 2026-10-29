"use client";

import type { StreamStatus } from "../client";
import { cls } from "./MessageList";

export interface TurnStatusProps {
  connection: StreamStatus;
  /** A turn is running. */
  working: boolean;
  /** Cancel was requested and has not taken effect yet. */
  cancelling: boolean;
  onCancel: () => void;
  className?: string;
}

/** Connection and progress line, with the Cancel button while a turn runs. */
export function TurnStatus({ connection, working, cancelling, onCancel, className }: TurnStatusProps) {
  let text = "";
  if (connection === "connecting") text = "Connecting…";
  else if (connection === "reconnecting") text = "Connection lost. Reconnecting…";
  else if (cancelling) text = "Cancelling…";
  else if (working) text = "Working…";
  return (
    <div role="status" className={cls("aicli-status", className)} data-connection={connection}>
      <span>{text}</span>
      {working && !cancelling && (
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      )}
    </div>
  );
}
