import { ConversationEvent } from "../client/types.js";
/** One tool call as shown in the transcript. */
export interface ToolView {
  name: string;
  /** What the call was given; replaced by its output once the call finishes. */
  summary?: string;
  done: boolean;
  isError?: boolean;
}
/** One turn: what the user said and how the assistant answered. */
export interface TurnView {
  id: string;
  user?: string;
  assistant: string;
  tools: ToolView[];
  status: "running" | "completed" | "failed" | "cancelled";
  /** Failure code when status is "failed", for example "quota_exceeded". */
  failure?: string;
  retryAfterSeconds?: number;
}
/**
 * Appends an event to an ordered list. An event whose seq is not above the last one is a
 * duplicate (a reconnect replayed it) and leaves the list untouched, same reference.
 */
export declare function addEvent(events: ConversationEvent[], e: ConversationEvent): ConversationEvent[];
/** Groups events into turns, in the order the turns started. Events without a turn are skipped. */
export declare function buildTurns(events: readonly ConversationEvent[]): TurnView[];
/** The turn that is still running, if any. */
export declare function runningTurn(turns: readonly TurnView[]): TurnView | undefined;
/** True when a turn failed because the CLI session is gone: the conversation can only be read. */
export declare function sessionExpired(turns: readonly TurnView[]): boolean;
/** A short, user-facing sentence for a failure code. */
export declare function failureMessage(code: string | undefined, retryAfterSeconds?: number): string;