import { ExportFormat } from "../client/types.js";
export interface ExportMenuProps {
  /** Resolves when the file was handed to the browser. */
  onExport: (format: ExportFormat) => Promise<void>;
  disabled?: boolean;
  className?: string;
}
/** Export buttons. Each runs onExport, which downloads through the authenticated client. */
export declare function ExportMenu({ onExport, disabled, className }: ExportMenuProps): import("react").JSX.Element;
/** Saves a blob through a temporary object URL. The URL is revoked right after the click. */
export declare function saveBlob(blob: Blob, filename: string): void;