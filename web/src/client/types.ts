/** Public types of the conversation API. They mirror the JSON the server sends. */

export type EventKind =
  | "user.message"
  | "turn.started"
  | "assistant.text"
  | "tool.use"
  | "tool.result"
  | "turn.completed"
  | "turn.failed"
  | "turn.cancelled";

export type TurnStatus = "running" | "completed" | "failed" | "cancelled";

export interface Conversation {
  id: string;
  title?: string;
  profile: string;
  /** False once the CLI session can no longer be continued; history stays readable. */
  resumable: boolean;
  /** The running turn, if any. */
  activeTurnId?: string;
  createdAt: string;
  updatedAt: string;
}

export interface Turn {
  id: string;
  status: TurnStatus;
  startedAt: string;
  finishedAt?: string;
}

export interface ToolInfo {
  name: string;
  summary?: string;
  isError?: boolean;
}

export interface ConversationEvent {
  /** Consecutive per conversation, starting at 1. */
  seq: number;
  turnId?: string;
  time: string;
  kind: EventKind;
  text?: string;
  tool?: ToolInfo;
  /** For turn.failed: `failure` (for example "quota_exceeded") and, when known, `retry_after_seconds`. */
  meta?: Record<string, string>;
}

export interface ConversationPage {
  items: Conversation[];
  nextCursor?: string;
}

export type ExportFormat = "md" | "json";

export interface ClientOptions {
  /** URL prefix where the host mounted the API, for example "/api/aicli". */
  baseUrl: string;
  /**
   * Extra headers for every request, for example `{ Authorization: "Bearer …" }`. Called per
   * request, so tokens can be refreshed. Never put credentials in the URL.
   */
  getHeaders?: () => Promise<Record<string, string>> | Record<string, string>;
  /** Pass "include" when the host authenticates with a cookie on another origin. */
  credentials?: RequestCredentials;
  /** Replaces the global fetch (tests, custom transports). */
  fetch?: typeof fetch;
}

/** The server answered with an error. `code` is the stable machine-readable reason. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}
