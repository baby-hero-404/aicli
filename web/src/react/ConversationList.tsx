"use client";

import { useEffect, useState } from "react";
import type { Conversation } from "../client";
import { useClient } from "./AicliProvider";
import { describe } from "./Conversation";
import { cls } from "./MessageList";

export interface ConversationListProps {
  /** Profile for new conversations; it must be on the server's allowlist. */
  profile: string;
  selectedId?: string;
  onSelect: (conversationId: string) => void;
  pageSize?: number;
  className?: string;
}

/** Your conversations, newest first, with a button to start a new one. */
export function ConversationList({ profile, selectedId, onSelect, pageSize = 20, className }: ConversationListProps) {
  const client = useClient();
  const [items, setItems] = useState<Conversation[]>([]);
  const [next, setNext] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    client.conversations.list({ limit: pageSize }, ac.signal).then(
      (page) => {
        setItems(page.items);
        setNext(page.nextCursor);
        setLoading(false);
      },
      (err: unknown) => {
        if (ac.signal.aborted) return;
        setError(describe(err));
        setLoading(false);
      },
    );
    return () => ac.abort();
  }, [client, pageSize]);

  async function loadMore() {
    if (!next) return;
    setLoading(true);
    try {
      const page = await client.conversations.list({ cursor: next, limit: pageSize });
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

  return (
    <nav className={cls("aicli-list", className)} aria-label="Conversations">
      <button type="button" onClick={() => void create()}>
        New conversation
      </button>
      {error && (
        <p role="alert" className="aicli-error">
          {error}
        </p>
      )}
      <ul>
        {items.map((c) => (
          <li key={c.id}>
            <button type="button" aria-current={c.id === selectedId ? "true" : undefined} onClick={() => onSelect(c.id)}>
              <span className="aicli-list-title">{c.title || "Untitled"}</span>
              <time dateTime={c.createdAt}>{new Date(c.createdAt).toLocaleString()}</time>
            </button>
          </li>
        ))}
      </ul>
      {!loading && items.length === 0 && !error && <p className="aicli-empty">No conversations yet.</p>}
      {next && (
        <button type="button" disabled={loading} onClick={() => void loadMore()}>
          Load more
        </button>
      )}
    </nav>
  );
}
