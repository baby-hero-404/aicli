# CLAUDE.md

Context for Claude Code in `github.com/baby-hero-404/aicli`.

- Commands: `make verify` (lint + Go tests + web checks), `make test`, `make lint`, `make tidy`, `make test-docker` (needs a Docker daemon and `AICLI_TEST_IMAGE`), `make web-verify` (pnpm; typecheck, eslint, vitest, build), `make web-example` (builds `examples/web` against the built `@aicli/ui`).
- Code rules: no global state, no `init()`, no `panic`, wrap errors with context, table-driven tests run with `-race`, Conventional Commits.
- aicli is application-agnostic: never reference a specific host application, database, user or tenant model in code, comments, docs or manifests.
- Layout rules: framework/vendor adapters in sub-packages named after what they adapt, mocks in `<pkg>/<pkg>mock`, dependencies point down, every package has a doc comment.
- Never mount anything from the host user's HOME into a sandbox.
- `conversation` must not import `transport/httpapi`; `run` must not import `conversation`; Go code never depends on `web/`. Nothing a client sends may reach `run.Request` except the prompt and an allowlisted profile name. New npm dependencies need approval; keep exact versions and the pnpm settings in `web/pnpm-workspace.yaml`.
- Host-installed CLIs (`toolchain`) are always mounted read-only; only `toolchain.Installer` writes the tools root, and it runs install steps inside a container with `npm --ignore-scripts`.
