# Proposal: Conversation UI

## Problem

Every service that embeds aicli has to rebuild the same things on top of `run.Runner`: a chat screen, streaming output, conversation history, CLI session resume, turn state and log export. `run.Runner.Run` only returns a `Result` after the CLI exits, so there is nothing to stream, and there is no notion of a conversation that outlives one run.

Because the UI runs in a browser and execution runs on the server, a naive implementation turns into a remote command endpoint: a client that can influence `Command`, `Args`, `Image` or mounts, or read another principal's conversation, can run arbitrary code with the host's credentials.

## Goal

A host service mounts one `http.Handler` and renders one React component, and gets: create a conversation, reopen an old one, send messages, watch output stream in, resume the CLI session, cancel a turn, and export the conversation, without writing any of that logic itself and without the browser ever controlling how the CLI is executed.

## Success

- A new host integrates the feature by implementing three small interfaces (`Store`, `Authorizer`, `Resolver`), mounting `httpapi.Handler`, and rendering `<Conversation>`; `examples/` proves it with no host code beyond those.
- Two independent consumers embed the same `@aicli/ui` version without forking it.
- The security test suite (see specs.md) passes: no request shape lets a client change command, args, image, mounts, credentials, or reach a conversation it is not authorized for.

## Assumptions

- Consumers of the UI use React (≥18). Non-React consumers can use the framework-free `@aicli/ui/client` and build their own view.
- The host already authenticates its users; aicli receives an opaque principal string and never sees passwords, sessions or user models.
- agy 1.4.8 streams NDJSON in print mode (`--output-format stream-json`): `init`, `step_update` and `result` events, with the conversation ID nested in each (schema recorded in design.md → Key Decisions; fixtures in `run/stream/agy/testdata/`). `--print-timeout` defaults to `0s` (wait for the turn). The schema is undocumented, so it is pinned by fixtures and re-checked on every agy upgrade.
- `agy --conversation <id>` resumes a previous conversation: verified on the host (turn 2 answered from turn 1's context, `num_turns: 2`). **Not yet verified: resuming in a fresh container** with the same `SessionDir` (mounted at `.gemini`) and workspace path. The base image pins agy 1.4.2, not 1.4.8, and testing needs a login inside the container (via `capture`), so it is a manual gate before Phase 2 ends.
- agy does not fail on an unknown `--conversation` ID: it exits 0 and starts a new conversation (see design.md), so the service must compare IDs.
- A conversation's `SessionDir` lives on storage visible to whichever backend instance runs its next turn (local disk for single-instance; shared volume for multi-instance). aicli does not move session state between machines.
- The two current consumers shape the integration surface:
  - one uses chi, Bearer tokens, and reads its own SSE streams with `fetch` + `Authorization` header (it explicitly avoids query-string tokens);
  - the other uses gin, an HttpOnly session cookie, and `EventSource`.
  Both web apps are Next.js 16 + React 19. Anything aicli ships must work under both auth models and both routers (`net/http` handler mounts in chi directly and in gin via `gin.WrapH`).

## Decisions

- **Option C (hybrid).** Conversations are structured chat over headless runs (`-p` + resume + stream). The PTY/WebSocket path in `capture` stays, but only for login. Users never get a raw terminal into a running CLI, so the sandbox is not the only defense.
- **The browser sends intent, never execution.** Request bodies carry only `profile` (allowlisted name), `message`, and an idempotency key. Workspace, image, credentials, mounts and session dir come from the host's server-side `Resolver`.
- **Host owns identity, authorization and storage** through interfaces (`Authenticator` func, `Authorizer`, `Store`). Default authorizer denies everything. aicli ships only `memstore` for tests and demos.
- **REST + SSE, not WebSocket.** Sending is a POST; output is an SSE stream with `id:` = per-conversation sequence number, so reconnect with `Last-Event-ID` replays from the store. SSE needs no extra Go dependency and survives proxies better.
- **SSE read with `fetch`, not `EventSource`; no stream tickets.** The client parses `text/event-stream` from a `fetch` response body, so every request — REST and stream — carries the host's normal credentials: a header from a host-supplied `getHeaders()` (Bearer hosts) or `credentials: "include"` (cookie hosts). This keeps one auth path on the server (the host `Authenticator`), puts no token in a URL, and lets the client send `Last-Event-ID` itself. `capture/ticket` stays for the login WebSocket only.
- **Opaque conversation IDs.** The CLI's own conversation ID is stored server side only and never serialized to a client.
- **History is separate from execution session.** Events are persisted as they stream; a conversation whose CLI session can no longer resume is still readable and exportable.
- **Normalized event contract, one decoder per CLI.** `run/stream` defines `Event` and `Decoder`; `run/stream/agy` is the only decoder in this task. Events live under `run/stream` (not `run/event.go`) so decoders can import them without an import cycle with `run`. The contract is CLI-neutral so other decoders can be added later without touching `conversation` or the UI.
- **Antigravity (agy) only.** The allowlisted profiles are `antigravity` (coding, tools on) and `antigravity_notools` (chat only, `--disable-slash-commands`). Both already exist in `profile` with `ResumeArgs` → `--conversation <id>`. Claude Code and Codex are out of scope.
- **agy arg placement is load-bearing.** agy parses flags with Go's `flag` package: `-p` takes the very next token as the prompt, and parsing stops at the first positional. `StreamArgs` therefore replaces the trailing `--output-format json` with `--output-format stream-json`, and `ResumeArgs` stays appended after it — never anything between `-p` and the prompt instruction.
- **One process per turn, not agy's `--input-format stream-json`.** agy can run several turns from NDJSON on stdin in one process, but that needs a long-lived container per conversation, ties a conversation to one backend instance, and makes cancel and crash recovery per-process. One run per turn + `--conversation` keeps every turn stateless on the server.
- **Fallback if agy stream-json is unusable** (schema unstable or missing the conversation ID): run with `--output-format json` and emit events only at turn end, so history, resume and export still ship; live streaming waits for a fixed agy. Decided in Phase 1 from the recorded fixtures.
- **Cancel works from any instance, through the store.** `Cancel` records the request (`Store.RequestCancel`); the instance running the turn cancels it immediately if local, otherwise on its next cancel poll (default 2 s). This replaces an earlier `409 not_local` design (rejected in review: behind a round-robin load balancer, cancel would fail most of the time and leak routing to the user) without adding a broker or requiring sticky sessions.
- **One SSE stream per open conversation, kept across turns.** Opened when the view mounts, before any send; heartbeats while idle. Sends never open streams, so there is no "events before the stream connects" race; reconnects replay from `Last-Event-ID`.
- **Refreshed credentials go back to the host.** `run.Result.UpdatedCredentialFiles` (an OAuth refresh during a turn) is passed to `Resolver.SaveCredentials` before the turn is finished; otherwise the next turn would re-inject the expired token and fail with `auth_invalid`.
- **`profile.StreamingArgs` owns the stream flag swap**, so `run` never edits flag lists by hand. It works on the `Args` the caller passed (hosts may customize them; `run.Request.Args` is not taken from the profile), replacing the `--output-format <fmt>` pair in place. This replaces the earlier `BuildArgs` idea, which would have rebuilt argv from the profile's own `Args` and ignored the host's.
- **Server-side export.** `GET /conversations/{id}/export?format=md|json` renders from stored, already-redacted events under the same authorization as reads, so export cannot leak more than the UI shows.
- **One npm package with subpath exports**: `@aicli/ui/client` (no React) and `@aicli/ui/react` (React as peer dependency). Unstyled components with CSS custom properties and class hooks; no CSS framework imposed on consumers.
- **pnpm, exact pins, release-age gate.** `web/` is a standalone pnpm project (`packageManager` pinned, `pnpm-lock.yaml` committed, `--frozen-lockfile` in CI and make targets). Every dependency is pinned to an exact version; pnpm's minimum release age is set to 7 days so a freshly published (possibly compromised) version is never resolved; dependency lifecycle scripts stay blocked (pnpm 12 denies builds by default; `allowBuilds` is never set, and the lockfile has no package that requires a build). In pnpm 12 `onlyBuiltDependencies` no longer exists; an explicit `minimumReleaseAge` is strict.
- **Unbundled ESM build that keeps `"use client"`.** Both consumers are Next.js; bundling into one file drops per-file directives. The build emits one file per module plus `.d.ts`, and a check fails the build if any `dist/react/*.js` lacks the directive.
- **Assistant text renders as text.** No `dangerouslySetInnerHTML` and no markdown renderer in MVP; output is untrusted.
- **Keep existing files in place.** `run/run.go` is extended, not split into runner/request/result files; splitting is churn without behavior change.
- **Tests live next to the code they test**, not in a top-level `tests/` tree: Go convention, and it keeps package-private helpers testable. Security tests are `transport/httpapi/security_test.go`; Docker integration tests keep the existing `AICLI_DOCKER_TESTS` gate.

### npm toolchain (latest stable that is ≥7 days old on 2026-10-29)

| Package | Version | Role | Why this one |
|---|---|---|---|
| `pnpm` | 12.15.0 | package manager | Latest major; 12.16.x is <7 days old |
| `typescript` | 6.0.3 | types | 7.0.x is out, but `typescript-eslint` 8.72 supports `<6.1` only; same version as one consumer |
| `tsdown` | 0.23.1 | library build (rolldown) | `tsup` README: "not actively maintained, consider tsdown"; `unbundle` mode and `.d.ts`; 0.23.2+ <7 days old |
| `vitest` | 5.0.5 | tests | Latest; same as one consumer (coverage plugin not added: not needed) |
| `jsdom` | 30.1.3 | DOM for tests | Latest |
| `@testing-library/react` / `dom` / `user-event` | 16.3.3 / 10.4.2 / 14.6.8 | component tests | Latest |
| `react`, `react-dom` | 19.3.1 (dev) | dev/test | Latest stable; peer range `^18.3.0 \|\| ^19.0.0` |
| `@types/react`, `@types/react-dom` | 19.3.0 | types | Latest |
| `eslint` / `@eslint/js` | 10.13.0 / 10.0.1 | lint (flat config) | Latest |
| `typescript-eslint` | 8.72.0 | TS lint | 8.72.1 is <7 days old |
| `eslint-plugin-react-hooks` | 7.1.1 | hooks rules | Latest; supports eslint 10 |
| `publint`, `@arethetypeswrong/cli` | 0.3.25, 0.18.5 | package `exports`/types correctness | Standard checks for a published library |
| `axe-core` | 4.14.0 | a11y assertions in tests | Latest; `vitest-axe` is unmaintained since 2025. `@testing-library/jest-dom` was not added; tests assert on DOM properties |
| `vite`, `@vitejs/plugin-react` | 8.3.5, 6.1.2 | `examples/web` only | Not installed: `examples/web` is deferred (see tasks) |

Versions are re-checked against the registry (and the 7-day rule) at install time; a newer version that passes the rule may replace one above.

## Trade-offs

- Structured chat cannot answer an interactive prompt mid-turn; the turn ends with `awaiting_input` (already classified by `rules`). Accepted: that is the safe behavior for an unattended CLI.
- SSE is one-way; cancel is a separate POST. Accepted: cancel is rare and a POST is simpler to authorize than a socket message.
- Live fan-out is in-process. On multi-instance deployments a client attached to a different instance than the one running the turn sees events by store polling (bounded delay, default 1 s) instead of instantly. Accepted for MVP; a pub/sub hook can be added behind `Store` later.
- Cross-instance cancel takes up to one cancel-poll interval (default 2 s) and costs one store query per instance per tick. Accepted: cheaper than a broker and invisible to users.
- An open stream per viewed conversation holds a connection while idle. Mitigated by a per-principal stream cap (default 5) and closing on unmount.
- agy's stream format is undocumented, so the decoder can break on an agy update. Mitigated by recorded fixtures, tolerant decoding, and the outcome still being decided by `run.Result`.
- React-only components exclude other frameworks from the ready-made UI. Mitigated by the framework-free client.
- Hosts must implement `Store` with an atomic turn claim. More work for them, but it is the only way to be correct across instances.
- TypeScript 6 instead of 7: give up the faster native compiler to keep type-aware linting.

## Non-goals

- Not optimizing for many concurrent viewers of one conversation; one principal at a time is the design target (multiple tabs of the same principal work).
- Not rendering rich tool UIs (diffs, file trees). Tool use shows name + truncated summary.
- Not making `memstore` production-grade (no persistence, single process).
- Not changing how `capture` login works.

## Out of Scope

- Decoders for Claude Code and Codex.
- Markdown/syntax-highlighted rendering of assistant output.
- Web Components / Lit build.
- Database-backed `Store` implementations.
- Continuing a non-resumable conversation by re-feeding its transcript into a new CLI session.
- Pub/sub fan-out, message brokers, sticky-session requirements, separate conversation microservice.
- Publishing `@aicli/ui` to a registry. Until then consumers depend on it by path (`link:` for pnpm, `file:` for npm) and run its build.

## Impact

### Components

- `sandbox`: optional output writer on `CommandRequest`; `docker`, `sandboxmock` and `StubRuntime` honor it.
- `profile`: `StreamArgs` for `antigravity` and `antigravity_notools`; `StreamingArgs`.
- `run`: streaming via `Request.OnEvent`; new `run/stream`, `run/stream/agy`.
- New `conversation`, `conversation/storetest`, `conversation/memstore`, `transport/httpapi`.
- New `web/` pnpm package and `examples/`.
- `Makefile`, `README.md`, `CLAUDE.md`, `docs/cli/antigravity.md`.

### Files

Created:
- `run/stream/stream.go`, `run/stream/lines.go`, `run/stream/stream_test.go`
- `run/stream/agy/agy.go`, `run/stream/agy/agy_test.go`, `run/stream/agy/testdata/*.jsonl`
- `conversation/doc.go`, `model.go`, `service.go`, `store.go`, `authorizer.go`, `resolver.go`, `events.go`, `errors.go`, `export.go`, `broker.go` + `_test.go` files
- `conversation/storetest/storetest.go`
- `conversation/memstore/memstore.go`, `memstore_test.go`
- `transport/httpapi/doc.go`, `handler.go`, `conversations.go`, `messages.go`, `events.go`, `middleware.go`, `dto.go` + `_test.go` files, `security_test.go`
- `web/package.json`, `web/pnpm-lock.yaml`, `web/pnpm-workspace.yaml` (pnpm settings), `web/tsconfig.json`, `web/tsdown.config.ts`, `web/eslint.config.js`, `web/vitest.config.ts`, `web/src/client/{api,events,types,index}.ts`, `web/src/react/{AicliProvider,Conversation,ConversationList,MessageList,MessageInput,TurnStatus,ExportMenu,index}.tsx`, tests under `web/src/**/*.test.ts(x)`
- `examples/go-server/main.go`, `examples/web/` (Vite + React)
- `docs/conversation.md` (integration guide)

Modified:
- `sandbox/sandbox.go`, `sandbox/docker/docker.go`, `sandbox/sandboxmock/sandboxmock.go`
- `profile/profile.go`
- `run/run.go`, `run/redact.go`
- `Makefile`, `README.md`, `CLAUDE.md`, `.gitignore`, `docs/cli/antigravity.md`

### Public API

Additive only:
- Go: `sandbox.CommandRequest.Output`, `profile.Profile.StreamArgs`, `profile.StreamingArgs`, `run.Request.OnEvent`, packages `run/stream`, `run/stream/agy`, `conversation`, `conversation/storetest`, `conversation/memstore`, `transport/httpapi`.
- HTTP (under the host's mount prefix): see design.md → Interfaces & Contracts.
- npm: `@aicli/ui/client`, `@aicli/ui/react`.

### Migration

None for existing users. Go: no new module dependencies (standard library `net/http`, `log/slog`). npm: the toolchain table above (approved by the user on 2026-10-29), all dev-only except the `react`/`react-dom` peer range.

Consumer-side work (not part of this repo, listed so it is not forgotten):
- A consumer that runs CLIs with `os/exec` instead of `run.Runner` + a `sandbox.Runtime` must switch to a runtime to use conversations; `conversation` depends on `run.Runner`.
- A consumer whose architecture test restricts aicli imports to one package must allow `conversation` and `transport/httpapi` in the package that wires them.
- A consumer on npm (not pnpm) depends on `@aicli/ui` with `file:`; it does not need pnpm itself.

### Backward Compatibility

All Go changes are additive fields/packages with zero-value behavior identical to today: a nil `Output`/`OnEvent` means no streaming and `Run` behaves exactly as before.
