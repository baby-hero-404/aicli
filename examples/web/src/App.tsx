import { createClient } from "@aicli/ui/client";
import { AicliProvider, Conversation, ConversationList } from "@aicli/ui/react";
import { useMemo, useState } from "react";

// What the demo server (examples/go-server) accepts as identity. A real host would send its own
// credentials here instead, for example `Authorization: Bearer …`.
const DEMO_USER_HEADER = "X-Demo-User";

export function App() {
  const [user, setUser] = useState("demo");
  const [selected, setSelected] = useState<string | undefined>();

  // A new client per user; the components reopen their requests when it changes.
  const client = useMemo(
    () =>
      createClient({
        baseUrl: "/api/aicli",
        getHeaders: () => ({ [DEMO_USER_HEADER]: user }),
      }),
    [user],
  );

  return (
    <div className="app">
      <header>
        <h1>aicli example</h1>
        <label>
          Demo user{" "}
          <input
            value={user}
            onChange={(e) => {
              setUser(e.target.value.trim());
              setSelected(undefined);
            }}
          />
        </label>
      </header>
      {user === "" ? (
        <p>Enter a user name.</p>
      ) : (
        <AicliProvider client={client}>
          <main>
            <ConversationList profile="antigravity" {...(selected ? { selectedId: selected } : {})} onSelect={setSelected} />
            {selected ? <Conversation conversationId={selected} /> : <p className="aicli-empty">Pick or start a conversation.</p>}
          </main>
        </AicliProvider>
      )}
    </div>
  );
}
