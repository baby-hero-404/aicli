// Post-build checks on dist/ that the bundler does not guarantee.
//
//  1. Every React module that uses hooks or context starts with a "use client" directive.
//     Next.js reads the directive per file, so it must survive the build (no bundling of
//     modules together).
//  2. dist/client never imports React: it is the framework-free part.
//  3. React itself is never bundled into the output.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const dist = new URL("../dist/", import.meta.url).pathname;
const problems = [];

function* files(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) yield* files(p);
    else if (p.endsWith(".js")) yield p;
  }
}

const usesReactState = /\b(useState|useEffect|useContext|createContext|useMemo|useRef|useReducer)\b/;
const directive = /^\s*(["'])use client\1/;

let checked = 0;
for (const file of files(join(dist, "react"))) {
  checked++;
  const src = readFileSync(file, "utf8");
  if (usesReactState.test(src) && !directive.test(src)) {
    problems.push(`${file}: uses React state/context but does not start with "use client"`);
  }
  if (/\bReactCurrentDispatcher\b|__SECRET_INTERNALS/.test(src)) problems.push(`${file}: React internals were bundled`);
}
for (const file of files(join(dist, "client"))) {
  checked++;
  const src = readFileSync(file, "utf8");
  if (/from\s+["']react(\/[^"']*)?["']/.test(src)) problems.push(`${file}: dist/client must not import React`);
  if (directive.test(src)) problems.push(`${file}: dist/client must not be marked "use client"`);
}

if (checked === 0) problems.push("dist/ has no JavaScript files; did the build run?");
if (problems.length > 0) {
  console.error(problems.join("\n"));
  process.exit(1);
}
console.log(`dist ok (${checked} files checked)`);
