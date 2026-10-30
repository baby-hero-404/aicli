# examples/web

A standalone Vite + React app that uses `@aicli/ui` (the API client and React components) against
`examples/go-server`. It shows the whole web side of a host: a client with `getHeaders`, the
provider, a conversation list and a conversation view.

Like the Go example, it is a demo: identity is a plain `X-Demo-User` header anyone can set.

## Run

```sh
make web-build                                              # @aicli/ui is linked from ../../web
go run ./examples/go-server -origin http://localhost:5173   # the server must allow the page's origin
cd examples/web && pnpm install --frozen-lockfile --ignore-scripts && pnpm dev
```

Open http://localhost:5173. Vite forwards `/api` to `127.0.0.1:8787`, so the browser sees one
origin and no CORS is involved. The server runs agy in Docker, so it needs the base image
(`make image-base`) and the CLI's login files (`-credentials`, see `examples/go-server`).

`make web-example` type-checks and builds the app; it is part of `make verify`.

## Notes

- `@aicli/ui` is a `link:` dependency because the package is not published yet. It has its own
  copy of React, so `vite.config.ts` sets `resolve.dedupe` to keep one copy in the bundle; without
  it, hooks fail with "Invalid hook call".
- The pnpm settings in `pnpm-workspace.yaml` match `web/` (7-day minimum release age, exact
  versions, no dependency build scripts). Keep them.
- To use a Bearer token or a cookie instead of the demo header, change `createClient` in
  `src/App.tsx`: `getHeaders: async () => ({ Authorization: … })`, or `credentials: "include"`.
