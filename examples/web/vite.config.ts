import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The browser talks to this dev server only; /api is forwarded to examples/go-server, so no CORS
// is involved. The server must allow this page's origin:
//   go run ./examples/go-server -origin http://localhost:5173
export default defineConfig({
  plugins: [react()],
  // @aicli/ui is linked from ../../web, which has its own copy of React. Two copies break hooks.
  resolve: { dedupe: ["react", "react-dom"] },
  server: {
    port: 5173,
    strictPort: true,
    proxy: { "/api": { target: "http://127.0.0.1:8787", changeOrigin: true } },
  },
});
