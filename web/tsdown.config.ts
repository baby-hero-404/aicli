import { defineConfig } from "tsdown";

// One output file per source file (unbundle): the React modules start with "use client", and a
// directive only survives when its module is not merged into another chunk. Next.js needs it.
export default defineConfig({
  entry: ["src/client/**/*.ts", "src/react/**/*.{ts,tsx}", "!src/**/*.test.{ts,tsx}", "!src/**/testutil.ts"],
  unbundle: true,
  format: "esm",
  dts: true,
  platform: "neutral",
  sourcemap: false,
  clean: true,
  outDir: "dist",
  deps: { neverBundle: ["react", "react/jsx-runtime"] },
  // rolldown warns about "use client" in unbundle mode although it keeps it; scripts/check-dist.mjs
  // verifies the directive survives in the output, which is what actually matters.
  checks: { moduleLevelDirective: false },
});
