"use client";

import { failureMessage, type TurnView } from "./model";

const pre = { whiteSpace: "pre-wrap", overflowWrap: "anywhere" } as const;

/**
 * The transcript. Everything is rendered as text: output from a CLI is untrusted, so it is
 * never interpreted as HTML or markdown.
 */
export function MessageList({ turns, className }: { turns: readonly TurnView[]; className?: string }) {
  return (
    <div role="log" aria-live="polite" aria-relevant="additions text" aria-label="Conversation" className={cls("aicli-messages", className)}>
      {turns.map((t) => (
        <article key={t.id} className="aicli-turn" data-status={t.status}>
          {t.user !== undefined && (
            <div className="aicli-user">
              <span className="aicli-author">You</span>
              <p style={pre}>{t.user}</p>
            </div>
          )}
          {t.tools.length > 0 && (
            <ul className="aicli-tools">
              {t.tools.map((tool, i) => (
                <li key={i} className="aicli-tool" data-done={tool.done} data-error={tool.isError ? "true" : undefined}>
                  <span className="aicli-tool-name">{tool.name}</span>
                  {tool.summary && <span className="aicli-tool-summary" style={pre}>{` ${tool.summary}`}</span>}
                </li>
              ))}
            </ul>
          )}
          {(t.assistant !== "" || t.status === "running") && (
            <div className="aicli-assistant">
              <span className="aicli-author">Assistant</span>
              <p style={pre}>{t.assistant}</p>
            </div>
          )}
          {t.status === "failed" && (
            <p role="alert" className="aicli-failure">
              {failureMessage(t.failure, t.retryAfterSeconds)}
            </p>
          )}
          {t.status === "cancelled" && <p className="aicli-cancelled">Cancelled.</p>}
        </article>
      ))}
    </div>
  );
}

export function cls(...names: (string | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}
