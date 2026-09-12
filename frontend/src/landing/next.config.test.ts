import { afterEach, describe, expect, it, vi } from "vitest";
import { resolve } from "node:path";

const serverRuntimeEnvironment = [
  "NEXT_PUBLIC_API_URL",
  "NEXT_PUBLIC_AO_AUTH_MODE",
  "AO_CLOUD_AUTH_MODE",
] as const;

const originalEnvironment = Object.fromEntries(
  serverRuntimeEnvironment.map((name) => [name, process.env[name]]),
);

async function loadConfig(environment: Partial<Record<(typeof serverRuntimeEnvironment)[number], string>> = {}) {
  for (const name of serverRuntimeEnvironment) {
    delete process.env[name];
  }
  Object.assign(process.env, environment);
  vi.resetModules();
  return (await import("./next.config")).default;
}

afterEach(() => {
  for (const name of serverRuntimeEnvironment) {
    if (originalEnvironment[name] === undefined) {
      delete process.env[name];
    } else {
      process.env[name] = originalEnvironment[name];
    }
  }
  vi.resetModules();
});

describe("Next configuration", () => {
  it("runs the local Cloud web app as a server", async () => {
    const config = await loadConfig({
      NEXT_PUBLIC_API_URL: "http://127.0.0.1:3010",
      NEXT_PUBLIC_AO_AUTH_MODE: "local",
    });

    expect(config.output).toBeUndefined();
    expect(config.trailingSlash).toBe(false);
    expect(config.turbopack?.root).toBe(resolve(process.cwd(), ".."));
  });

  it("keeps the marketing site as a static export", async () => {
    const config = await loadConfig();

    expect(config.output).toBe("export");
    expect(config.trailingSlash).toBe(true);
  });
});
