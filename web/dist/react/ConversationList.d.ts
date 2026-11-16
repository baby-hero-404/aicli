export interface ConversationListProps {
  /** Profile for new conversations; it must be on the server's allowlist. */
  profile: string;
  selectedId?: string;
  onSelect: (conversationId: string) => void;
  pageSize?: number;
  className?: string;
}
/** Your conversations, newest first, with a button to start a new one. */
export declare function ConversationList({ profile, selectedId, onSelect, pageSize, className }: ConversationListProps): import("react").JSX.Element;