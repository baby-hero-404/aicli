import { StreamStatus } from "../client/events.js";
export interface TurnStatusProps {
  connection: StreamStatus;
  /** A turn is running. */
  working: boolean;
  /** Cancel was requested and has not taken effect yet. */
  cancelling: boolean;
  onCancel: () => void;
  className?: string;
}
/** Connection and progress line, with the Cancel button while a turn runs. */
export declare function TurnStatus({ connection, working, cancelling, onCancel, className }: TurnStatusProps): import("react").JSX.Element;