"use client";

import { useState } from "react";
import type { ExportFormat } from "../client";
import { cls } from "./MessageList";

export interface ExportMenuProps {
  /** Resolves when the file was handed to the browser. */
  onExport: (format: ExportFormat) => Promise<void>;
  disabled?: boolean;
  className?: string;
}

/** Export buttons. Each runs onExport, which downloads through the authenticated client. */
export function ExportMenu({ onExport, disabled = false, className }: ExportMenuProps) {
  const [busy, setBusy] = useState<ExportFormat | null>(null);
  const [failed, setFailed] = useState(false);

  async function run(format: ExportFormat) {
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

  return (
    <div className={cls("aicli-export", className)}>
      <button type="button" disabled={disabled || busy !== null} onClick={() => void run("md")}>
        Export Markdown
      </button>
      <button type="button" disabled={disabled || busy !== null} onClick={() => void run("json")}>
        Export JSON
      </button>
      {failed && (
        <span role="alert" className="aicli-error">
          Export failed.
        </span>
      )}
    </div>
  );
}

/** Saves a blob through a temporary object URL. The URL is revoked right after the click. */
export function saveBlob(blob: Blob, filename: string): void {
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
