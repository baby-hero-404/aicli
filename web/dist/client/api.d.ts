import { ApiError, ClientOptions, Conversation, ConversationEvent, ConversationPage, ExportFormat, Turn } from "./types.js";
export interface SendResult {
  turn: Turn;
  /** True when the server had already seen this clientMessageId and started nothing new. */
  duplicate: boolean;
}
export interface ExportedFile {
  blob: Blob;
  /** Suggested file name from the server. */
  filename: string;
}
export interface Client {
  readonly options: Readonly<ClientOptions>;
  conversations: {
    create(input: {
      profile: string;
      title?: string;
    }, signal?: AbortSignal): Promise<Conversation>;
    list(input?: {
      cursor?: string;
      limit?: number;
    }, signal?: AbortSignal): Promise<ConversationPage>;
    get(id: string, signal?: AbortSignal): Promise<Conversation>;
  };
  messages: {
    /** `clientMessageId` makes a retry safe; one is generated when omitted. */
    send(id: string, input: {
      message: string;
      clientMessageId?: string;
    }, signal?: AbortSignal): Promise<SendResult>;
  };
  turns: {
    /** Resolves when the request was accepted; the outcome arrives on the event stream. */
    cancel(conversationId: string, turnId: string, signal?: AbortSignal): Promise<void>;
  };
  events: {
    list(id: string, input?: {
      after?: number;
      limit?: number;
    }, signal?: AbortSignal): Promise<ConversationEvent[]>;
  };
  /** Downloads an export through fetch, so the host's auth header is sent. */
  exportFile(id: string, input: {
    format: ExportFormat;
    turnId?: string;
  }, signal?: AbortSignal): Promise<ExportedFile>;
  /** @internal Authenticated fetch used by the event stream. */
  request(path: string, init: RequestInit): Promise<Response>;
}
/** Creates an API client. It never throws on construction. */
export declare function createClient(options: ClientOptions): Client;
/** Turns an error response into an ApiError, using the server's error envelope when present. */
export declare function readError(res: Response): Promise<ApiError>;
/** A fresh idempotency key: letters, digits, "-" and "_", at most 64 characters. */
export declare function newClientMessageId(): string;