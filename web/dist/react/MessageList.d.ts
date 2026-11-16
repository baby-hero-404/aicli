import { TurnView } from "./model.js";
/**
 * The transcript. Everything is rendered as text: output from a CLI is untrusted, so it is
 * never interpreted as HTML or markdown.
 */
export declare function MessageList({ turns, className }: {
  turns: readonly TurnView[];
  className?: string;
}): import("react").JSX.Element;
export declare function cls(...names: (string | undefined)[]): string;