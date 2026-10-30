# Expected Behavior: Conversation UI

Paths below are relative to the host's mount prefix for `httpapi.Handler`. "Authorized" means the host `Authenticator` returned a principal and the host `Authorizer` allowed the action for that principal and conversation.

## Scenario: Create a conversation

**When:**
- An authenticated principal POSTs `/conversations` with `{"profile": "antigravity", "title"?: "..."}` and the profile is in the handler's allowlist and has a stream decoder (`antigravity` or `antigravity_notools` in this task).

**Then:**
- `Authorizer` is asked for `create` (no conversation ID yet).
- A conversation is stored with a new opaque ID (≥128 bits of randomness, URL-safe), owner principal, profile, status `idle`, `resumable = true`, no CLI session ID.
- Response `201` with the public conversation DTO. No CLI session ID, session dir, workspace or credential appears in it.

## Scenario: Send the first message

**When:**
- An authorized principal POSTs `/conversations/{id}/messages` with `{"message": "...", "clientMessageId": "<uuid>"}` and no turn is running.

**Then:**
- The service atomically claims a new turn (status `running`) via `Store.ClaimTurn`.
- `Resolver.Resolve` returns the base `run.Request` (workspace, image, credentials, session dir, env) from server-side state; the service sets only `Profile`, `Command`/`Args`+`StreamArgs` from `profile`, `Prompt`, `ResumeSessionID` and `OnEvent`.
- Response `202` with `{turnId}` immediately; the run continues in the background.
- Events `user.message`, `turn.started`, then decoded `assistant.text` / `tool.use` / `tool.result`, then `turn.completed` are appended to the store with strictly increasing `seq` and published to live subscribers.
- The agy conversation ID from the final object is stored on the conversation (server side only).
- The argv is `agy --dangerously-skip-permissions -p <prompt-file instruction> --output-format stream-json` (plus `--disable-slash-commands` for `antigravity_notools`): nothing sits between `-p` and its value.

## Scenario: Send a follow-up (resume)

**When:**
- A message is sent to a conversation that has a stored agy conversation ID and `resumable = true`.

**Then:**
- The run appends `profile.ResumeArgs(profile, id)` (`--conversation <id>`) after the output-format flag, and uses the same `SessionDir` and workspace mount path the previous turns used.
- If agy reports a different conversation ID, the stored one is replaced.

## Scenario: Reopen an old conversation

**When:**
- An authorized principal GETs `/conversations/{id}` and `/conversations/{id}/events?after=0` (history) — or the UI lists `/conversations` and opens one.

**Then:**
- The full event history is returned from the store, oldest first, paginated by `after` (default 0) + `limit` (1–500, default 200).
- `GET /conversations` returns only conversations the principal is authorized to `read`, newest first, paginated by an opaque `cursor` + `limit` (1–100, default 20).

## Scenario: Live stream (one per conversation, across turns)

**When:**
- `<Conversation>` mounts, and the client opens `GET /conversations/{id}/stream` with its normal credentials (header or cookie) and `Last-Event-ID` = the highest seq it already has (0 for none). This happens **before** the user can send anything.

**Then:**
- Each SSE message has `id: <seq>`, `event: <type>`, `data: <json>`.
- The server replays every stored event with `seq > Last-Event-ID`, then switches to live events, with no gap and no duplicate (handover algorithm in design.md → Subscribe).
- The stream stays open across turns: idle periods carry a heartbeat comment at least every 15 s. It closes only when the client aborts (unmount, conversation switch), on authorization loss, or on server shutdown.
- A send never opens a stream; it relies on the already-open one. Events emitted between `202` and a late (re)connect are not lost because the reconnect replays from `Last-Event-ID`.

## Scenario: Reconnect after a drop

**When:**
- The connection drops after event seq 41 and the client reconnects with `Last-Event-ID: 41`.

**Then:**
- Events 42.. are delivered exactly once, in order, followed by live events. The client retries with exponential backoff (max 30 s) and stops on `401`/`404`.

## Scenario: Cancel a running turn (any instance)

**When:**
- An authorized principal POSTs `/conversations/{id}/turns/{turnId}/cancel` while that turn is `running`, and the request may land on any backend instance.

**Then:**
- The service records the request with `Store.RequestCancel(turnID)` and returns `202`.
- The instance running the turn cancels the run's context: immediately if it is the same instance, otherwise within the cancel poll interval (default 2 s; it checks `Store.CancelRequested` for its running turns).
- The sandbox stops the process, the turn becomes `cancelled`, a `turn.cancelled` event is appended, and the conversation returns to `idle`. The UI learns the outcome from the stream, not from the `202`.
- Cancelling a turn that is not running returns `409 turn_not_running` and changes nothing. A second cancel of the same running turn returns `202` again (idempotent).

## Scenario: Export

**When:**
- An authorized principal GETs `/conversations/{id}/export?format=md` or `format=json` (optionally `&turnId=…`).

**Then:**
- Output is rendered from stored events only, with `Content-Disposition: attachment`.
- The UI downloads through the client's `fetch` (so Bearer hosts send their header) and saves it with a Blob URL. It never navigates to the export URL.
- JSON export is the event list plus conversation metadata, using the same DTOs as the API.
- Export works whether or not the conversation is resumable.

## Scenario: Client retries a send

**When:**
- The same principal POSTs the same `clientMessageId` to the same conversation twice (e.g. network retry).

**Then:**
- The second request returns the same `{turnId}` with `200` and starts no new run.

## Failure Scenario: Unauthenticated request

**When:**
- `Authenticator` returns an error for any endpoint.

**Then:**
- `401`, no store access, no `Authorizer` call.

## Failure Scenario: Not authorized

**When:**
- `Authorizer` denies the action, or returns an error.

**Then:**
- `404` for anything addressed by conversation ID (do not confirm existence), `403` for `create`/`list`. No events or metadata are returned. The denial is audit-logged.

## Failure Scenario: Client tries to control execution

**When:**
- A request body contains any field other than the documented ones (e.g. `command`, `args`, `image`, `workspace`, `env`, `mounts`, `sessionDir`, `credentialFiles`, `resumeSessionId`).

**Then:**
- `400 unknown_field`, nothing is stored or run. (Decoding uses `DisallowUnknownFields`.)

## Failure Scenario: Profile not allowed

**When:**
- `profile` is not in the handler allowlist, or has no stream decoder.

**Then:**
- `400 profile_not_allowed`.

## Failure Scenario: Turn already running

**When:**
- A message is sent (new `clientMessageId`) while another turn of that conversation is `running`, from this or any other backend instance.

**Then:**
- `409 turn_in_progress`; `ClaimTurn` rejects it atomically; no second run starts.

## Failure Scenario: Message too large or rate limited

**When:**
- The request body exceeds the configured max (default 64 KiB, message ≤ 32 KiB), or the principal exceeds the send rate (default 10/min) or concurrent-turn limit (default 2).

**Then:**
- `413 message_too_large` or `429 rate_limited` with `Retry-After`; nothing is stored.

## Failure Scenario: Authorization revoked during a stream

**When:**
- A stream is open and the principal loses `read` on the conversation.

**Then:**
- The server re-checks `Authorizer` at least every 60 s on open streams and closes the stream on denial; the next reconnect gets `404`.

## Failure Scenario: Disallowed Origin

**When:**
- Any request carries an `Origin` header that is not in the configured allowlist, or a state-changing request (POST) has no `Origin`.

**Then:**
- `403 origin_not_allowed`.
- A POST without `Origin` (a non-browser client) is allowed only if `Config.AllowNoOrigin` is true (default false). Browsers always send `Origin` on POST.
- A GET without `Origin` is allowed. Browsers omit `Origin` on same-origin GETs (stream, events, export), GETs have no side effects, and CORS still stops cross-origin reads. If `Sec-Fetch-Site` is present and equals `cross-site`, the request must have an allowed `Origin`.

## Failure Scenario: Invalid query or field

**When:**
- `limit` is out of range or not an integer, `after` is negative or not an integer, `cursor` cannot be decoded, `format` is not `md`/`json`, `title` is longer than 255 runes, or `clientMessageId` does not match `^[A-Za-z0-9_-]{1,64}$`.

**Then:**
- `400 invalid_query` (query parameters) or `400 invalid_field` (body fields); nothing is stored.

## Failure Scenario: Credential refresh during a turn

**When:**
- agy refreshes its OAuth token during a run, so `run.Result.UpdatedCredentialFiles` is non-empty.

**Then:**
- The service calls `Resolver.SaveCredentials` with the updated files before it marks the turn finished, so the next turn (on any instance) gets the fresh token.
- If `SaveCredentials` fails, the turn still finishes with its own outcome. The failure is logged at ERROR and counted, and the turn gets the `credentials_not_saved` flag so the host can alert. The updated content is never logged.

## Failure Scenario: CLI session no longer resumable

**When:**
- A resumed run reports a `result.conversation_id` different from the one requested (agy does not fail: it exits 0 and silently starts a new conversation, warning `conversation "<id>" not found` on stderr), or `SessionDir` is gone.

**Then:**
- The turn fails with code `session_expired`, the conversation is marked `resumable = false`, and further sends return `409 not_resumable`. History and export still work. The accidental new agy conversation's ID and output are discarded (never stored as the conversation's session, never shown as an answer).

## Failure Scenario: CLI outcome is quota, auth or awaiting input

**When:**
- `run.Result` reports `QuotaExceeded`, `AuthInvalid` or `AwaitingInput`.

**Then:**
- The turn fails with code `quota_exceeded` (with `retryAfterSeconds` when `QuotaCooldown` is known), `auth_invalid`, or `awaiting_input`; the conversation stays `resumable`.

## Failure Scenario: Process or server dies mid-turn

**When:**
- The backend instance running a turn crashes or restarts, so the turn stays `running` with no live owner.

**Then:**
- `Service.RecoverStale` (called by the host at startup and periodically) marks every `running` turn whose lease expired (default 2 min, renewed every 30 s while the run is alive) as `failed` with code `interrupted` and appends `turn.failed`. The conversation becomes `idle` and can be resumed.

## Failure Scenario: Decoder sees an unparsable line

**When:**
- The CLI writes a line that is not valid JSON or has an unknown `type`.

**Then:**
- It is skipped (counted in a metric), never forwarded raw. The turn's outcome is still decided by `run.Result`.

## Invariants

- No HTTP request field can reach `run.Request` except `Prompt` (from `message`) and `Profile` (from an allowlisted name).
- The message never appears in argv: it is written to the prompt file that `promptFileInstruction` points agy at.
- A CLI conversation ID, `SessionDir`, workspace path, credential, env value or image name is never serialized to a client.
- Every byte of CLI output sent to a client or written to the store has passed through redaction as a complete line/event; partial chunks never leave the server.
- At most one turn per conversation is `running` at any time, across all backend instances.
- Credentials a CLI refreshed during a turn are handed to the host before the next turn of that conversation can be claimed.
- `seq` is strictly increasing per conversation with no gaps visible to clients; the stored event log is append-only.
- Every read, send, cancel, export, list and stream is authorized; the zero-value `Authorizer` denies.
- No credential or token ever appears in a URL.
- A conversation's history remains readable and exportable regardless of its `resumable` state.
- Nothing from the host user's HOME is mounted. `sandbox.ValidateRequest` rejects a mount source that is that HOME or a directory above it, so a `Resolver` mistake fails the turn instead of exposing it.

## Rules

- Conversation IDs and turn IDs are opaque random strings; never sequential, never derived from host data.
- Tool-use inputs and tool results are truncated (default 4 KiB per field) before storage, after redaction.
- Audit log entries (via injected `*slog.Logger`) record principal, action, conversation ID, turn ID, outcome — never message text or output.
- Cookie-authenticated hosts get CSRF protection from the Origin check on every state-changing request; the handler never accepts state changes via GET.
- Assistant output is rendered as text in the UI; no HTML injection path.

## Constraints

- aicli stays application-agnostic: no user, tenant or database concepts in code; identity is an opaque `Principal` string.
- No new Go module dependencies; `net/http` + `log/slog` only.
- `web/` uses pnpm with a committed lockfile, exact version pins, a 7-day minimum release age, and blocked dependency lifecycle scripts.
- Every module under `@aicli/ui/react` ships with a `"use client"` directive in its built output.
- No global state, no `init()`, no `panic` (project rules).
- `conversation` must not import `transport/httpapi`; `run` must not import `conversation`; Go code must not depend on anything under `web/`.
