/** One message of a text/event-stream response. */
export interface SseMessage {
  id?: string;
  event?: string;
  data: string;
}
/**
 * Incremental parser for text/event-stream. Feed it text as it arrives, in chunks of any size;
 * it returns the messages completed by that chunk. Comment lines and unknown fields are
 * ignored; a message without data is not dispatched, as the spec requires.
 */
export declare class SseParser {
  #private;
  feed(chunk: string): SseMessage[];
}