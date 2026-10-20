# CLAUDE.md

Context for Claude Code in `github.com/baby-hero-404/aicli`.

- Commands: `make test`, `make lint`, `make tidy`, `make test-docker` (needs a Docker daemon and `AICLI_TEST_IMAGE`).
- Code rules: no global state, no `init()`, no `panic`, wrap errors with context, table-driven tests run with `-race`, Conventional Commits.
- aicli is application-agnostic: never reference a specific host application, database, user or tenant model in code, comments, docs or manifests.
- Layout rules: framework/vendor adapters in sub-packages named after what they adapt, mocks in `<pkg>/<pkg>mock`, dependencies point down, every package has a doc comment.
- Never mount anything from the host user's HOME into a sandbox.
