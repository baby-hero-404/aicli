"use client";
import { createContext, useContext } from "react";
import { jsx } from "react/jsx-runtime";
// src/react/AicliProvider.tsx
const ClientContext = createContext(null);
/** Makes an API client (see createClient) available to the components below it. */
function AicliProvider({ client, children }) {
	return /* @__PURE__ */ jsx(ClientContext.Provider, {
		value: client,
		children
	});
}
/** The client given to the nearest AicliProvider. */
function useClient() {
	const client = useContext(ClientContext);
	if (!client) throw new Error("Wrap your components in <AicliProvider client={…}>.");
	return client;
}
export { AicliProvider, useClient };
