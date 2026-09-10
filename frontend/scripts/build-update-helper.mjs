import { spawnSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
export function helperBuildOptions(arch, root = frontend) {
  const cpu = arch === "arm64" ? "arm64" : arch === "x64" ? "x86_64" : undefined;
  if (!cpu) throw new Error(`Unsupported macOS update helper architecture: ${arch}`);
  const source = path.join(root, "native", "update-helper");
  const output = path.join(root, "update-helper", "ao-update-progress");
  return {
    output,
    args: ["swiftc", "-target", `${cpu}-apple-macosx11.0`, "-O",
      path.join(source, "UpdateProgressState.swift"), path.join(source, "main.swift"),
      "-o", output],
  };
}

export function buildUpdateHelper({ arch = process.arch, platform = process.platform, run = spawnSync } = {}) {
  if (platform !== "darwin") throw new Error("The macOS update helper must be built on macOS");
  const { output, args } = helperBuildOptions(arch);
  mkdirSync(path.dirname(output), { recursive: true });
  const moduleCache = path.join(homedir(), ".ao", "build-cache", "update-helper", arch);
  mkdirSync(moduleCache, { recursive: true });
  const result = run("xcrun", [...args, "-module-cache-path", moduleCache], { stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Swift update helper compilation failed (${result.status ?? result.signal})`);
  return output;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    if (args.length !== 0 && (args.length !== 2 || args[0] !== "--arch")) throw new Error("Usage: build-update-helper.mjs [--arch arm64|x64]");
    buildUpdateHelper({ arch: args[1] ?? process.arch });
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
