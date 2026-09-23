import { cpSync, existsSync, mkdirSync, rmSync } from "node:fs";
import path from "node:path";

const root = process.cwd();
const nested = path.join(root, ".next/standalone/private/ao-cloud");
const dest = path.join(root, ".next/standalone");
const destNext = path.join(dest, ".next");

if (!existsSync(path.join(nested, ".next/server/pages-manifest.json"))) {
  process.exit(0);
}

const nestedNext = path.join(nested, ".next");
if (existsSync(destNext)) {
  rmSync(destNext, { recursive: true, force: true });
}
mkdirSync(dest, { recursive: true });
cpSync(nestedNext, destNext, { recursive: true });

for (const name of ["server.js", "node_modules"]) {
  const from = path.join(nested, name);
  const to = path.join(dest, name);
  if (!existsSync(from)) {
    continue;
  }
  if (existsSync(to)) {
    rmSync(to, { recursive: true, force: true });
  }
  cpSync(from, to, { recursive: true });
}

console.log("Flattened Next standalone output for OpenNext.");
