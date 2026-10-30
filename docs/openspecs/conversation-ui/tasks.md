# Implementation Map: Conversation UI

**Goal:** A host mounts `httpapi.Handler` and renders `<Conversation>`, and gets secure, resumable, streaming CLI conversations with history and export.
**Tech Stack:** Go 1.27 standard library (`net/http`, `log/slog`), existing aicli packages; agy ≥1.4.8; pnpm 12, TypeScript 6, React 19 (peer `^18.3 || ^19`), tsdown, vitest + Testing Library, eslint 10. Exact versions are in proposal.md → npm toolchain.

Scope: the `antigravity` and `antigravity_notools` profiles only. Every phase ends green on `make verify`.

---

## Phase 0: Tooling

### Add a `verify` target and fix lint

**Why:** The Stop hook gates on `make verify`, and `make lint` passes even when files aren't gofmt'd.

**Files:** `Makefile`, `profile/profile.go`, `profile/profile_test.go`

**Changes:**
- [x] `lint` fails when `gofmt -l .` prints anything
- [x] gofmt the two `profile` files
- [x] `verify: lint test` (web targets are added in Phase 4)

**Verify:**
- [x] `make verify` passes; introducing a mis-formatted file makes it fail

## Phase 1: Streaming execution

### Verify the agy stream format and resume (go/no-go gate)

**Why:** agy's stream-json schema is undocumented. Every later phase depends on it, so it has to be confirmed from real output first. This step also decides between streaming and the json fallback.

**Files:** `run/stream/agy/testdata/agy-<version>-{basic,tools,resume,not-found,error}.jsonl`

**Changes:**
- [x] Record `agy -p … --output-format stream-json` on the host (agy 1.4.8, scratch dir) for: a plain answer, a resumed turn, a tool call, an unknown conversation ID. Sanitized fixtures are in `run/stream/agy/testdata/agy-1.4.8-*.jsonl` (+ `.stderr`).
- [x] Write down the event types, where the conversation ID lives and how text arrives (design.md → Key Decisions)
- [x] Record the decision: streaming, not the json fallback (design.md)
- [x] Auth error observed end to end in the example server (logged-out container → `auth_invalid`). The raw output was not saved as a fixture; quota output is covered by `rules` tests.
- [ ] **Manual gate before Phase 2 ends:** resume in a **fresh container** with the same `SessionDir` and workspace path. It needs a login inside the container (via `capture`) and an image with agy ≥1.4.8 (the base image pins 1.4.2). Note whether a project ID or absolute path also has to match.

**Verify:**
- [x] Resuming on the host answers with context from the earlier turn (`num_turns: 2`)
- [x] An unknown ID is observed to start a new conversation with exit 0 (handled by ID comparison)
- [ ] Resuming in a fresh container answers with context from the earlier turn

### Event contract and decoder

**Why:** It gives every CLI a normalized event shape and keeps raw output from ever reaching clients.

**Depends on:** fixtures

**Files:** `run/stream/stream.go`, `run/stream/lines.go`, `run/stream/stream_test.go`, `run/stream/agy/agy.go`, `run/stream/agy/agy_test.go`

**Changes:**
- [x] `Event`, `Kind`, `Tool`, `Decoder`
- [x] A line splitter (an `io.Writer` that emits complete lines and caps line length, default 1 MiB)
- [x] An agy decoder: assistant text, tool use/result summaries (truncated to 4 KiB), the final object carrying the conversation ID, and unknown types skipped
- [x] Detect an expired agy conversation by comparing `result.conversation_id` with the requested ID (agy exits 0 and starts a new conversation). Done in `run` (`Result.SessionMismatch`, which also stops the run and suppresses the wrong session's events) and mapped to `session_expired` by the Service; the stderr `conversation "<id>" not found` warning is only a secondary signal, so no `rules` change is planned

**Verify:**
- [x] Table-driven tests over every fixture
- [x] A line split across writes is decoded once
- [x] Invalid JSON returns an error the caller can count and skip

### Runtime output tee

**Why:** `run` needs the CLI's output while it is still running, not only after it exits.

**Files:** `sandbox/sandbox.go`, `sandbox/docker/docker.go`, `sandbox/sandboxmock/sandboxmock.go`, the matching tests

**Changes:**
- [x] `CommandRequest.Output io.Writer` (optional)
- [x] docker: tee stdout into `Output` from the existing `stdcopy` loop
- [x] sandboxmock and StubRuntime write their stdout to `Output`

**Verify:**
- [x] A mock run delivers its scripted stdout to `Output` in order
- [x] A nil `Output` keeps today's behavior, with the existing tests unchanged
- [x] The Docker integration test (gated) streams lines before the container exits

### Runner streaming

**Why:** This wires the pieces together while keeping the zero-value behavior unchanged.

**Files:** `profile/profile.go`, `run/run.go`, `run/redact.go`, `run/run_test.go`

**Changes:**
- [x] `Profile.StreamArgs` (set for `antigravity` and `antigravity_notools`)
- [x] `profile.StreamingArgs(ref, args)`: swap the output-format pair in place; `ErrStreamingUnsupported` for profiles without `StreamArgs`
- [x] `run` applies `StreamingArgs` to the caller's args before appending `ResumeArgs` and `ExtraArgs`
- [x] `Request.OnEvent`: pipe Output → splitter → decoder → redact → OnEvent
- [x] Redact event text (per complete line, so a secret split across deltas is caught) and tool summaries; `Result.SessionID` and `Result.SkippedLines`

**Verify:**
- [x] A fake API key arrives at `OnEvent` already redacted, including when split across two text deltas
- [x] `OnEvent` set on a profile without `StreamArgs` (or an unknown one) fails with `ErrInvalidRequest` before the runtime is called
- [x] Tests of `StreamingArgs` for both profiles and of the generated script, fresh and resumed: `-p` is directly followed by the prompt-file instruction, the output format is `stream-json` only when streaming, and `--conversation <id>` comes last
- [x] The existing `run` tests still pass unchanged (non-streaming argv is identical to today's)
- [x] `Result` classification still works when streaming (`classify` is shared; fixture-backed)

## Phase 2: Conversation service

### Domain types and store contract

**Why:** This is the reusable core. The store contract is what keeps multi-instance deployments correct.

**Files:** `conversation/{doc,model,errors,store,authorizer,resolver,events}.go`, `conversation/storetest/storetest.go`, `conversation/memstore/{memstore,memstore_test}.go`

**Changes:**
- [x] `Conversation`, `Turn` (status: running, completed, failed, cancelled), `Event` (seq, turnId, time, stream.Event), and the error values
- [x] `Store`, `Authorizer`, `Resolver` interfaces; a deny-all default
- [x] `storetest.Run(t, newStore)`, a contract suite covering concurrent `ClaimTurn`, idempotent duplicates, seq ordering, stale turns, and `RequestCancel` / `CancelRequested` (idempotent; `ErrTurnNotRunning` on a finished turn)
- [x] memstore passes the contract suite

**Verify:**
- [x] `go test -race ./conversation/...` runs the contract suite against memstore, including 50 concurrent claims of which exactly 1 wins

### Service

**Why:** It owns the turn lifecycle so that hosts don't have to.

**Depends on:** Phase 1, domain types

**Files:** `conversation/{service,broker,export}.go` + tests

**Changes:**
- [x] Create / Get / List / Events with authorization
- [x] Send: authorize → validate → ClaimTurn → Resolve → run in a goroutine, with lease renewal and batched AppendEvents → `SaveCredentials` (if `UpdatedCredentialFiles` is non-empty) → FinishTurn, storing the agy conversation ID
- [x] Map `run.Result` to failure codes (quota_exceeded, auth_invalid, awaiting_input, session_expired → resumable=false, interrupted, run_failed)
- [x] Cancel: `Store.RequestCancel`, then the local cancel func if there is one; a `CancelPoll` ticker cancels local turns that another instance marked
- [x] Subscribe: the handover algorithm from design.md (register on the broker → replay from the store → drain with a seq watermark → fill gaps from the store), plus `EventPoll` for turns running on another instance
- [x] RecoverStale
- [x] Export to md and json from stored events

**Verify:**
- [x] Every Scenario and Failure Scenario in specs.md that the service owns has a table-driven test using sandboxmock and memstore
- [x] A `Resolver` output that mounts HOME is rejected: the existing policy did not do this, so `sandbox.ValidateRequest` now rejects a mount source that is HOME or one of its ancestors. Covered in `sandbox` (`TestValidateMounts_RejectsHostHome`) and through `Service` (`TestResolverMountingHostHomeIsRejected`, with a runtime that applies `ValidateRequest`). Not caught: a source that is a subdirectory of HOME holding secrets, such as `~/.ssh`.
- [x] Handover race: a test hook pauses Subscribe between broker registration and `ListEvents` while the publisher appends events. The subscriber receives every seq exactly once and in order. Run it 200 times under `-race`.
- [x] A slow subscriber whose buffer overflows still receives a gap-free sequence (the catch-up comes from the store), and the publisher never blocks
- [x] Cross-instance cancel: two `Service` instances share one memstore. Cancelling from B stops the turn running on A within `CancelPoll` plus a margin, and the turn ends `cancelled`.
- [x] Credentials: a sandboxmock result with `UpdatedCredentialFiles` reaches `SaveCredentials` before the turn is finished. If `SaveCredentials` fails, the turn keeps its outcome and gets `credentials_not_saved`, and the log contains no credential content.

### Deferred from Phase 2 (tracked, not forgotten)

- [ ] _Deferred (agreed):_ OpenTelemetry metrics and the `conversation.send` span (design.md → Observability). Audit logging through `slog` is done; metrics need a decision on the meter wiring first.
- [ ] _Deferred (agreed):_ Per-principal active-turn limit is per instance (in memory). Fine for MVP; a store-backed count is needed for a hard global limit.
- [ ] Manual gate: resume in a fresh container (Phase 1).


## Phase 3: HTTP API

### Handler, middleware and SSE

**Why:** It is the mountable integration point for both consumers' routers.

**Depends on:** Phase 2

**Files:** `transport/httpapi/{doc,handler,conversations,messages,events,middleware,dto}.go` + tests, `transport/httpapi/security_test.go`

**Changes:**
- [x] `NewHandler(Config)` on a stdlib `ServeMux` with method patterns
- [x] Middleware: Authenticate; Origin rules from specs.md (POST requires an allowed Origin, GET is checked when Origin is present or `Sec-Fetch-Site: cross-site`); MaxBytesReader; per-principal rate and active-turn limits; a stream cap
- [x] Strict JSON decoding plus field bounds (`title` ≤255 runes, the `clientMessageId` pattern, message ≤32 KiB); query bounds (`limit`, `after`, `cursor`, `format`, `Last-Event-ID`) → `invalid_query`
- [x] DTOs that never include the agy conversation ID, paths, env or credentials
- [x] SSE that stays open across turns: `id`/`event`/`data`, `Last-Event-ID` replay, 15 s heartbeat, 60 s re-authorization, flush per event, closed on client abort or server shutdown
- [x] Error envelope and codes

**Cleanup:**
- [x] Checked `capture/wsgorilla.OriginAllowed` for sharing: **not shared**. Its rules differ (an empty Origin is always allowed, which this API must refuse for POST), and importing it would pull `gorilla/websocket` into every consumer of `httpapi`. `httpapi` has its own 15-line `originAllowed`.

**Verify:**
- [x] `security_test.go` covers (all green, and mutation-checked: removing strict decoding, the origin check, the content-type check, the POST-needs-Origin rule or the cross-site GET rule each turn the suite red):
  - unknown fields (`command`, `args`, `image`, `workspace`, `env`, `mounts`, `sessionDir`, `credentialFiles`, `resumeSessionId`) → 400
  - another principal's conversation → 404 on every endpoint
  - a bad Origin → 403 on POST and on GET
  - a POST without Origin → 403
  - a same-origin GET without Origin → allowed
  - `Sec-Fetch-Site: cross-site` without Origin → 403
  - an oversize body → 413, the rate limit → 429
  - `limit=0`, `limit=501`, `after=-1`, `limit=abc`, a garbage cursor → 400 `invalid_query`
  - a 256-rune title or a 65-character `clientMessageId` → 400 `invalid_field`
  - no response body ever contains the agy conversation ID or the resolver's paths
- [x] Reconnecting with `Last-Event-ID` produces no gap and no duplicate (httptest)
- [x] One stream stays open through two consecutive turns and delivers both, with a heartbeat in between
- [x] The handler works when mounted under a prefix with `http.StripPrefix` (stdlib test; chi and gin mounting is checked in Phase 5, so no router dependency comes into aicli)

**Added during implementation (not in the first draft):**
- [x] `Content-Type: application/json` is required on POST bodies (`415 unsupported_media_type`). A cross-site form or `text/plain` POST cannot carry it without a CORS preflight, which is one more CSRF barrier for cookie-authenticated hosts.
- [x] Path IDs must match `^[A-Za-z0-9_-]{1,128}$`, otherwise `404`.
- [x] `X-Content-Type-Options: nosniff` on every response; `Cache-Control: no-store` on JSON, exports and streams.

### Example host

**Files:** `examples/go-server/main.go`

**Changes:**
- [x] memstore, `OwnerOnly`, a resolver (per-conversation workspace and session dir, credentials from a directory, `SaveCredentials` writing back), a docker runtime, and a header-based demo Authenticate (clearly marked unsafe to expose)

**Verify:**
- [x] Run for real against Docker (agy not logged in): create → send → SSE stream with heartbeats → `turn.failed` with `auth_invalid` after ~60 s; audit log lines contain no message text; no container left behind. Documented in `docs/conversation.md` (Phase 6).

## Phase 4: Web package

### Client

**Why:** It is the framework-free core that all UI builds on and that non-React hosts can use.

**Files:** `web/package.json`, `web/tsconfig.json`, `web/src/client/{types,api,events,index}.ts` + tests

**Changes:**
- [x] Set `"packageManager": "pnpm@12.15.0"`, `"type": "module"`, `"sideEffects": false`, and `exports` for `./client` and `./react` (ESM + `types`)
- [x] pnpm settings: minimum release age of 7 days, dependency build scripts blocked, `save-exact`. Check the setting names against the pnpm 12 docs before writing them.
- [x] Re-check every version in proposal.md against the registry and the 7-day rule, then `pnpm add -D --save-exact …` and commit `pnpm-lock.yaml`
- [x] After installing, run `git status` and check for any new AI-instruction or config files (rule R2)
- [x] Types that mirror the DTOs; `createClient` with `getHeaders` / `credentials` / `fetch`
- [x] A fetch-based SSE parser with `Last-Event-ID`, exponential backoff and an AbortSignal

**Verify:**
- [x] vitest: the parser handles split chunks, multi-line data and comments; reconnect sends the last ID; a 401/404 stops retrying

### React components

**Files:** `web/src/react/{AicliProvider,Conversation,ConversationList,MessageList,MessageInput,TurnStatus,ExportMenu,index}.tsx` + tests

**Changes:**
- [x] `"use client"` on every component; no `window` access at import time
- [x] Unstyled components with `aicli-*` class names and CSS custom properties
- [x] `<Conversation>` opens one stream on mount (Send stays disabled until it connects) and aborts it on unmount or when `id` changes
- [x] The input is disabled while a turn is running or the conversation is not resumable; Cancel shows "cancelling…" until `turn.cancelled` arrives on the stream
- [x] `ExportMenu` uses `client.exportFile` (fetch + Blob) plus `URL.createObjectURL`, and revokes the URL after the download; it never sets `href` to the API URL

**Verify:**
- [x] Testing Library: output containing `<script>` renders as text; the full flow works against a mocked client; axe finds no violations

### Build and Makefile

**Files:** `Makefile`, `.gitignore`, `examples/web/`

**Changes:**
- [x] `tsdown.config.ts`: unbundled ESM output plus `.d.ts`, with entries `src/client/index.ts` and `src/react/index.ts`, and `react` external
- [x] `web-install` (`pnpm --dir web install --frozen-lockfile`), `web-lint` (`tsc --noEmit` + eslint), `web-test` (vitest), `web-build` (tsdown, then publint and attw); `verify` includes all of them
- [x] A Vite + React example in `examples/web` (own lockfile and the same pnpm settings; `@aicli/ui` linked by path; `vite@8.3.5`, `@vitejs/plugin-react@6.1.2`, checked against the registry and the 7-day rule). `make web-example` builds it and is part of `make verify`. Not run against a live server (needs Docker and an agy login).

**Verify:**
- [x] `make verify` is green on a clean checkout
- [x] Every `dist/react/*.js` starts with `"use client"`, and `dist/client` has no React import (both checks are scripted in `web-build`)
- [x] publint and attw report no errors for the `./client` and `./react` exports

**Verified beyond the unit tests:** the built `dist/client` was run from Node against the real Go example server in Docker: stream, idempotent retry, history equal to the stream, export and error mapping all worked. Mutation checks: 6 deliberate breakages of the React code (HTML injection, send before connected, missing pending-turn bridge, no dedupe, lost input on failure, plain-link export) each turn the suite red.


## Phase 5: Consumer validation

### Embed in both consumers (in their own repos, via go.work replace)

**Why:** The success criterion is reuse without forking.

**Done in a trial (uncommitted in the consumers' working trees):**
- [x] Bearer + chi consumer: `Authenticate` from its bearer middleware, a database-backed `Store` (a new numbered migration), a `Resolver` on the organization's provider credential, the API mounted at `/organizations/{orgID}/chat/*` (admin only), a `/chat` page.
- [x] Cookie + gin consumer: `Authenticate` from its session cookie, a database-backed `Store` (a new numbered migration), a `Resolver` on the stored CLI login (a credential lease with write-back of a refreshed token), the API mounted at `/v1/chat/*` (operators only), its architecture test extended to allow aicli in the one package that wires it, a `/chat` page.
- [x] `@aicli/ui` installed by path as a copy: `file:` (pnpm) and `npm install --install-links file:` (npm). `link:` and plain npm `file:` break React (see friction).
- [x] Outdated CLI code in the consumers cleaned up: the bearer consumer replaced its copies of the ticket store, websocket terminal and PTY glue with `capture/ticket`, `capture.Session` and `capture/wsgorilla`, and derives CLI state directories from `profile.StateDirs` (its own table had codex wrong). Not replaced: its code-step runner (a partial reimplementation of `run.Runner`, entangled with MCP and logging: a separate migration) and the cookie consumer's host-side login manager and redaction (different behavior on purpose).

**Verify (done):**
- [x] `storetest.Run` passes on both Postgres stores (13 tests, including concurrent claim and gap-free append).
- [x] Through each consumer's real router and a real `http.Server` with a short `WriteTimeout`: auth and roles are enforced, a foreign Origin is refused, create, send and stream work, the stream outlives the timeout, the sandbox receives the stored credential and a refreshed token is stored back.
- [x] Each real binary run once with its real migrations and the real Docker sandbox: the turn ran agy and ended `auth_invalid` (no login was available).
- [ ] **Not done:** a turn that answers, resume and export against a logged-in agy; the browser UI in a real browser; both consumers' own `make verify`/CI; committing and releasing them. All need an agy login or a decision on release order.

**Friction fed back (all addressed in this repo unless noted):**
- [x] `capture.Session` could not test a stored login (no files in HOME first) and `wsgorilla` returned everything in the workspace, credential included: added `RunOptions.Files`, `Handler.Seed` and `Handler.DiscardFiles`.
- [x] Two copies of React with `link:`/symlinked installs: documented.
- [x] The same-origin default fails behind a Host-rewriting proxy, and `AllowNoOrigin` is needed for bearer-token scripts: documented.
- [x] A router-wide request timeout ends the event stream: documented.
- [ ] Both consumers wrote the same ~330-line SQL `Store`. A reference `conversation/pgstore` on `database/sql` would remove that; its own tests need a Postgres driver, i.e. a new Go dependency, so it waits for approval.
- [ ] Neither consumer can merge before aicli is tagged: their `go.mod`, `go.sum` and `vendor/` still point at v0.1.2, and `go.work` is local.
- [ ] A logged-out agy took about a minute to give up in the sandbox in one run (not timed in the other): the UI shows nothing meanwhile.

## Phase 6: Documentation sync

**Files:** `README.md`, `CLAUDE.md`, `docs/conversation.md`, `docs/cli/antigravity.md`

**Changes:**
- [x] Add the new packages to the README table; a security model section
- [x] An integration guide: interfaces, Store requirements (atomic claim, the `(conversation_id, seq)` index), SessionDir on shared storage, mounting in chi and gin
- [x] Add the web make targets to CLAUDE.md
- [x] `docs/cli/antigravity.md`: fix flags that are stale against agy 1.4.8 (`--print-timeout` now defaults to `0s`, `--effort` is `low|medium|high|xhigh|max`, `--mode` is `accept-edits|auto|plan`), and document the stream-json schema recorded in Phase 1

**Verify:**
- [x] Every exported symbol has a doc comment; `go doc ./conversation` reads correctly

---

## Rollback Plan

All changes are additive and opt-in: hosts that don't mount `httpapi.Handler` or set `OnEvent` are unaffected. Rolling back means a consumer removes the mount and pins the previous aicli tag. No migrations are owned by aicli; host-side tables are created by the host and can be dropped independently.
