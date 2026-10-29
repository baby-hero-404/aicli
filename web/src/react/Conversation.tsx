"use client";

import { useEffect, useMemo, useState } from "react";
import {
  ApiError,
  streamEvents,
  type Conversation as ConversationInfo,
  type ConversationEvent,
  type ExportFormat,
  type StreamStatus,
} from "../client";
import { useClient } from "./AicliProvider";
import { ExportMenu, saveBlob } from "./ExportMenu";
import { MessageInput } from "./MessageInput";
import { cls, MessageList } from "./MessageList";
import { addEvent, buildTurns, runningTurn, sessionExpired } from "./model";
import { TurnStatus } from "./TurnStatus";

export interface ConversationProps {
  conversationId: string;
  className?: string;
}

/**
 * A chat view for one conversation: history, live output, sending, cancelling and exporting.
 * It opens a single event stream when mounted and keeps it across turns; switching
 * `conversationId` starts over with the new conversation.
 */
export function Conversation({ conversationId, className }: ConversationProps) {
  // Keyed so that all state resets when the conversation changes.
  return <ConversationView key={conversationId} conversationId={conversationId} {...(className ? { className } : {})} />;
}

function ConversationView({ conversationId, className }: ConversationProps) {
  const client = useClient();
  const [events, setEvents] = useState<ConversationEvent[]>([]);
  const [connection, setConnection] = useState<StreamStatus>("connecting");
  const [info, setInfo] = useState<ConversationInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pendingTurn, setPendingTurn] = useState<string | null>(null);
  const [cancellingTurn, setCancellingTurn] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    const report = (err: unknown) => {
      if (!ac.signal.aborted) setError(describe(err));
    };
    client.conversations.get(conversationId, ac.signal).then(setInfo, report);
    streamEvents(client, conversationId, {
      signal: ac.signal,
      onEvent: (e) => setEvents((prev) => addEvent(prev, e)),
      onStatus: setConnection,
    }).catch(report);
    return () => ac.abort();
  }, [client, conversationId]);

  const turns = useMemo(() => buildTurns(events), [events]);
  const running = runningTurn(turns);
  // Between the server accepting a message and its first event arriving there is no running turn
  // in the transcript yet; the pending turn id bridges that gap so the box stays disabled.
  const awaitingFirstEvent = pendingTurn !== null && !turns.some((t) => t.id === pendingTurn);
  const working = running !== undefined || awaitingFirstEvent;
  const expired = sessionExpired(turns) || info?.resumable === false;
  const canSend = connection === "open" && !working && !expired;

  async function send(message: string) {
    setError(null);
    try {
      const { turn } = await client.messages.send(conversationId, { message });
      setPendingTurn(turn.id);
    } catch (err) {
      setError(describe(err));
      throw err; // lets the input put the text back
    }
  }

  function cancel() {
    const id = running?.id ?? pendingTurn;
    if (!id) return;
    setCancellingTurn(id);
    client.turns.cancel(conversationId, id).catch((err: unknown) => {
      setCancellingTurn(null);
      setError(describe(err));
    });
  }

  async function exportAs(format: ExportFormat) {
    const file = await client.exportFile(conversationId, { format });
    saveBlob(file.blob, file.filename);
  }

  let hint: string | undefined;
  if (expired) hint = "This conversation can no longer be continued.";
  else if (connection !== "open") hint = "Waiting for the connection…";

  const activeTurn = running?.id ?? pendingTurn;
  return (
    <section className={cls("aicli-conversation", className)} aria-label={info?.title || "Conversation"}>
      <div className="aicli-toolbar">
        <ExportMenu onExport={exportAs} disabled={turns.length === 0} />
      </div>
      <MessageList turns={turns} />
      <TurnStatus
        connection={connection}
        working={working}
        cancelling={cancellingTurn !== null && cancellingTurn === activeTurn}
        onCancel={cancel}
      />
      {error && (
        <p role="alert" className="aicli-error">
          {error}
        </p>
      )}
      <MessageInput onSend={send} disabled={!canSend} {...(hint ? { hint } : {})} />
    </section>
  );
}

/** A user-facing sentence for an error; details of unexpected failures are not shown. */
export function describe(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "turn_in_progress":
        return "A message is already being processed. Wait for it to finish.";
      case "rate_limited":
        return "You are sending too fast. Wait a moment and try again.";
      case "not_resumable":
        return "This conversation can no longer be continued.";
      case "message_too_large":
        return "That message is too long.";
      case "unauthenticated":
        return "You are signed out. Sign in again to continue.";
      case "not_found":
      case "forbidden":
        return "This conversation does not exist, or you do not have access to it.";
      case "turn_not_running":
        return "The turn had already finished.";
      default:
        return err.status >= 500 ? "The server had a problem. Try again in a moment." : err.message;
    }
  }
  return "Something went wrong. Check your connection and try again.";
}
