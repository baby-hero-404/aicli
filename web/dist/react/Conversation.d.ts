export interface ConversationProps {
  conversationId: string;
  className?: string;
}
/**
 * A chat view for one conversation: history, live output, sending, cancelling and exporting.
 * It opens a single event stream when mounted and keeps it across turns; switching
 * `conversationId` starts over with the new conversation.
 */
export declare function Conversation({ conversationId, className }: ConversationProps): import("react").JSX.Element;
/** A user-facing sentence for an error; details of unexpected failures are not shown. */
export declare function describe(err: unknown): string;