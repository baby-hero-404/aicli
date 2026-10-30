# Conversations: integration guide

Add a chat UI over a headless CLI to your service. You write authentication, authorization and a
database adapter; aicli provides the turn lifecycle, streaming, history, resume, cancel, export
and the React components.

```
Browser ── @aicli/ui ──HTTP/SSE──► transport/httpapi ──► conversation.Service ──► run.Runner ──► sandbox
                                        ▲                      │ Store · Authorizer · Resolver
                                  your Authenticate            └── implemented by you
```

Supported CLI today: Antigravity (`antigravity`, `antigravity_notools`). Other profiles cannot
stream yet and are rejected at start-up.

## What you implement

| Interface | Job | Notes |
|---|---|---|
| `Authenticate(*http.Request) (Principal, error)` | Who is calling | Cookie, bearer token, anything. The principal is an opaque string. |
| `conversation.Authorizer` | May this principal do this action on this conversation? | Default denies everything. `conversation.OwnerOnly{}` allows each principal its own conversations. |
| `conversation.Resolver` | How a turn runs: workspace, image, credentials, session directory | Built from server-side state only. `SaveCredentials` receives tokens the CLI refreshed. |
| `conversation.Store` | Durable state | Run `storetest.Run` against it. `conversation/memstore` is a reference. |

### Store requirements

- `ClaimTurn` must be **atomic across all your instances** (a conditional insert/update in your
  database). It is what guarantees one running turn per conversation. In-process locks are not enough.
- `AppendEvents` must assign **consecutive** `Seq` per conversation, also under concurrent
  appends. Add an index on `(conversation_id, seq)`.
- `FinishTurn` updates the turn and the conversation in one transaction.
- Events and conversations may contain user text; encrypt at rest if your data policy requires it.

### Resolver requirements

- `SessionDir` must be **stable per conversation** and visible to every instance that may run its
  next turn (a shared volume when you run more than one instance). Use
  `sandbox.SessionDir(root, profile, conversationID)`; generated IDs are valid directory names.
  If the directory is gone the turn fails with `session_expired` and the conversation becomes
  read-only; its history stays available.
- Credentials: put the account's login files in `CredentialFiles` (keys relative to HOME). If the
  CLI refreshes a token during a turn, `SaveCredentials` gets the new files before the turn ends;
  store them or the next turn replays an expired token.
- A host that already has its own migrations needs a number past its latest one (the consumers
  hit a collision with an existing `…0021`).
- Never mount anything from your users' HOME. The sandbox rejects a mount whose host source is
  the HOME of the user running the server, or a directory above it such as `/` (the turn then
  fails with `run_failed`); a directory inside HOME, such as your data root, is allowed.

## Go side

```go
svc, err := conversation.NewService(conversation.Config{
    Runner:     &run.Runner{Runtime: dockerRuntime},
    Store:      myStore,
    Authorizer: conversation.OwnerOnly{},
    Resolver:   myResolver,
    Profiles:   []string{"antigravity"},   // what clients may pick
    Logger:     logger,                    // audit lines; never message text or output
})

api, err := httpapi.NewHandler(httpapi.Config{
    Service:        svc,
    Authenticate:   func(r *http.Request) (conversation.Principal, error) { /* your auth */ },
    AllowedOrigins: []string{"https://app.example.com"},
})
mux.Handle("/api/aicli/", http.StripPrefix("/api/aicli", api))   // net/http, chi
// gin (a sketch; not exercised by aicli's own tests): router.Any("/api/aicli/*any", gin.WrapH(http.StripPrefix("/api/aicli", api)))

go func() { for range time.Tick(time.Minute) { svc.RecoverStale(ctx) } }() // after a crash
defer svc.Close(shutdownCtx)                                               // interrupts running turns
```

`examples/go-server` is a complete, runnable host (do not expose it: its authentication is a
plain header).

Operational notes:

- **Origins.** A write is refused unless its `Origin` is allowed. The default (same origin: the
  Origin's host must equal the request's Host) does not hold behind a proxy that rewrites the Host,
  such as a Next.js `rewrites()` to the API: list the web's origin in `AllowedOrigins`. A request
  without an `Origin` (curl, a script) is refused unless `AllowNoOrigin` is set; set it for APIs
  authenticated by a bearer token, not for cookie authentication.
- **Router timeouts.** A middleware that gives every request a deadline (chi's `Timeout`, a
  `context.WithTimeout`) ends the event stream when it expires. Exclude `…/stream` from it. The
  server's own `WriteTimeout` is not a problem: the handler lifts it for the stream, also through
  gin and chi wrappers (tested in two consumers).
- **Streams are long-lived.** Do not put a write timeout on the server that serves them, and make
  sure a reverse proxy does not buffer `text/event-stream` (the handler sends
  `X-Accel-Buffering: no`).
- **Several instances** are supported by design and covered by tests with two `Service` instances sharing one store (not yet against a real shared database): any instance can start, cancel (via the store, within `CancelPoll`,
  default 2 s) or follow a turn (it polls the store every `EventPoll`, default 1 s). The
  per-principal active-turn and stream limits are per instance.
- `RecoverStale` marks turns whose instance died (lease of 2 minutes expired) as `interrupted`.

## Retention (the host's job)

aicli never deletes conversations; your database and your policy do. Two rules keep the log valid:

- **Remove whole conversations, not slices of them.** Delete or archive a conversation with all its
  turns and events together (for example one that was idle for six months, after exporting it with
  `Service.Export` in JSON to cold storage). Dropping events by *event time* (`DROP PARTITION` on a
  table partitioned by month) removes the start of conversations that are still in use, which breaks
  the Store contract (`seq` is consecutive from 1 and the log is append-only) and the replay of
  history. It also cannot work with the usual `(conversation_id, seq)` primary key, because a
  partition key has to be part of it.
- **If the table needs partitioning, partition by conversation.** Put the conversation's creation
  month (or a hash of `conversation_id`) in the events table and in its key, so a partition holds
  whole conversations; archiving an old conversation set and dropping its partition is then instant
  and cannot cut a live conversation.

Workspaces and CLI sessions on disk are cleaned by the `janitor` (see the README). It keeps what
changed within the TTL; pass `JanitorConfig.Keep` to also keep the ones whose conversation still
exists in your database.

## Web side

```tsx
import { createClient } from "@aicli/ui/client";
import { AicliProvider, Conversation, ConversationList } from "@aicli/ui/react";

const client = createClient({
  baseUrl: "/api/aicli",
  getHeaders: async () => ({ Authorization: `Bearer ${await getToken()}` }), // bearer hosts
  // credentials: "include",                                                   // cookie hosts on another origin
});

export function Workspace({ id, setId }: { id?: string; setId: (id: string) => void }) {
  return (
    <AicliProvider client={client}>
      <ConversationList profile="antigravity" selectedId={id} onSelect={setId} />
      {id && <Conversation conversationId={id} />}
    </AicliProvider>
  );
}
```

- Components are unstyled and expose `aicli-*` class names (`aicli-messages`, `aicli-turn`,
  `aicli-user`, `aicli-assistant`, `aicli-tools`, `aicli-input`, `aicli-status`, `aicli-export`,
  `aicli-list`, `aicli-error`, `aicli-hint`) and `data-status` attributes to style against.
- Every React module is marked `"use client"`, so they work in the Next.js App Router. Import
  from `@aicli/ui/client` alone for a non-React view.
- All requests, including the event stream, go through `fetch` with your headers; no token is ever
  placed in a URL. Exports are downloaded through `fetch` and saved from a Blob.
- Output from the CLI is rendered as text, never as HTML or markdown.

Until the package is published, depend on it by path after `make web-build`, in a way that makes
a **copy** (not a symlink): `file:../aicli/web` with pnpm (pnpm copies it and resolves the React peer
to your app's React), `npm install --install-links file:../aicli/web` with npm (plain `file:` makes a
symlink in npm). A symlinked package resolves `react` from its own `node_modules`, the app then
loads two copies of React and every hook fails with "Invalid hook call" or `useContext` of null. If
you do link it (`link:` with pnpm), make the bundler resolve `react` and `react-dom` to one copy
(`resolve.dedupe` in Vite). After rebuilding the package, install again to refresh the copy.

`examples/web` is a complete Vite + React app built this way (`make web-example` builds it). To try
it, run `go run ./examples/go-server -origin http://localhost:5173`, then `pnpm dev` in
`examples/web` and open http://localhost:5173; Vite forwards `/api` to the server.

## HTTP API

Prefix is wherever you mount it. All bodies are JSON (`Content-Type: application/json`); unknown
fields are rejected.

| Method and path | Body or query | Result |
|---|---|---|
| `POST /conversations` | `{profile, title?}` (title ≤ 255 chars) | `201` conversation |
| `GET /conversations` | `cursor`, `limit` 1–100 (default 20) | `{items, nextCursor?}` |
| `GET /conversations/{id}` | | conversation |
| `POST /conversations/{id}/messages` | `{message ≤ 32 KiB, clientMessageId ^[A-Za-z0-9_-]{1,64}$}` | `202` turn; `200` if the key was seen |
| `POST /conversations/{id}/turns/{turnId}/cancel` | | `202`; idempotent |
| `GET /conversations/{id}/events` | `after` ≥ 0, `limit` 1–500 (default 200) | `{items}` |
| `GET /conversations/{id}/stream` | header `Last-Event-ID` | `text/event-stream`, open across turns |
| `GET /conversations/{id}/export` | `format=md\|json`, `turnId?` | attachment |

Errors: `{"error":{"code","message"}}`. Codes: `unauthenticated` 401, `forbidden` 403,
`origin_not_allowed` 403, `not_found` 404 (also for conversations you may not see),
`turn_in_progress` / `turn_not_running` / `not_resumable` 409, `message_too_large` 413,
`unsupported_media_type` 415, `rate_limited` 429, `unknown_field` / `invalid_field` /
`invalid_query` / `profile_not_allowed` 400, `internal` 500.

Event kinds: `user.message`, `turn.started`, `assistant.text`, `tool.use`, `tool.result`,
`turn.completed`, `turn.failed`, `turn.cancelled`. A `turn.failed` carries `meta.failure`:
`quota_exceeded` (+ `retry_after_seconds` when known), `auth_invalid`, `awaiting_input`,
`session_expired`, `interrupted` or `run_failed`.

## Security checklist for hosts

- Run `storetest.Run` on your `Store`; a non-atomic `ClaimTurn` allows two simultaneous runs.
- Set `AllowedOrigins`. For cookie authentication also keep the cookie `SameSite=Lax` or stricter;
  state-changing requests additionally require an allowed `Origin` and a JSON content type.
- Keep `Authorizer` strict; it is checked on every request and re-checked every minute on open streams.
- Treat `Resolver.SaveCredentials` input as secrets; store them encrypted.
- Do not log request bodies or event text; aicli's own logs never contain them.
- Rate limits are per principal and per instance; put a gateway limit in front for a global one.
