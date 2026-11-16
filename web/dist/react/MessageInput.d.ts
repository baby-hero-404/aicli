export interface MessageInputProps {
  onSend: (message: string) => void | Promise<void>;
  disabled?: boolean;
  /** Why sending is unavailable, shown under the box. */
  hint?: string;
  maxLength?: number;
  className?: string;
}
/** A message box. Enter inserts a line break; Ctrl/⌘+Enter or the button sends. */
export declare function MessageInput({ onSend, disabled, hint, maxLength, className }: MessageInputProps): import("react").JSX.Element;