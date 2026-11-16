import { ConversationEvent } from "./types.js";
import { Client } from "./api.js";
export type StreamStatus = "connecting" | "open" | "reconnecting" | "closed";
export interface StreamOptions {
  /** Highest seq already held; the server replays everything after it. Default 0 (all history). */
  lastEventId?: number;
  /** Aborting ends the stream and resolves the returned promise. */
  signal: AbortSignal;
  onEvent: (event: ConversationEvent) => void;
  onStatus?: (status: StreamStatus) => void;
  /** Called for each failed connection attempt that will be retried. */
  onRetry?: (error: unknown, attempt: number) => void;
  /** Reconnect delay: doubles from initialMs up to maxMs, with jitter. Defaults 500 ms, 30 s. */
  backoff?: {
    initialMs?: number;
    maxMs?: number;
  };
}
/**
 * Follows a conversation's event stream until the signal aborts. One call serves a whole
 * conversation: it stays connected across turns, reconnects after a drop, and resumes from the
 * last event it delivered, so every event is passed to `onEvent` exactly once and in order.
 *
 * Rejects with an ApiError on a status that retrying cannot fix (401, 403, 404, 400).
 */
export declare function streamEvents(client: Client, id: string, opts: StreamOptions): Promise<void>;