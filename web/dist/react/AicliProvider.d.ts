import { Client } from "../client/api.js";
import { ReactNode } from "react";
/** Makes an API client (see createClient) available to the components below it. */
export declare function AicliProvider({ client, children }: {
  client: Client;
  children: ReactNode;
}): import("react").JSX.Element;
/** The client given to the nearest AicliProvider. */
export declare function useClient(): Client;