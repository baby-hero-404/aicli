"use client";

import { createContext, useContext, type ReactNode } from "react";
import type { Client } from "../client";

const ClientContext = createContext<Client | null>(null);

/** Makes an API client (see createClient) available to the components below it. */
export function AicliProvider({ client, children }: { client: Client; children: ReactNode }) {
  return <ClientContext.Provider value={client}>{children}</ClientContext.Provider>;
}

/** The client given to the nearest AicliProvider. */
export function useClient(): Client {
  const client = useContext(ClientContext);
  if (!client) throw new Error("Wrap your components in <AicliProvider client={…}>.");
  return client;
}
