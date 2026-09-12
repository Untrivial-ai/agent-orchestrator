import { afterEach, describe, expect, it, vi } from "vitest";

const { authkitMiddleware } = vi.hoisted(() => ({
  authkitMiddleware: vi.fn(() => vi.fn()),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({ authkitMiddleware }));

const originalAuthEnvironment = {
  cloud: process.env.AO_CLOUD_AUTH_MODE,
  public: process.env.NEXT_PUBLIC_AO_AUTH_MODE,
};

async function loadProxy(environment: {
  AO_CLOUD_AUTH_MODE?: string;
  NEXT_PUBLIC_AO_AUTH_MODE?: string;
}) {
  delete process.env.AO_CLOUD_AUTH_MODE;
  delete process.env.NEXT_PUBLIC_AO_AUTH_MODE;
  Object.assign(process.env, environment);
  vi.resetModules();
  return import("./proxy");
}

afterEach(() => {
  if (originalAuthEnvironment.cloud === undefined) {
    delete process.env.AO_CLOUD_AUTH_MODE;
  } else {
    process.env.AO_CLOUD_AUTH_MODE = originalAuthEnvironment.cloud;
  }
  if (originalAuthEnvironment.public === undefined) {
    delete process.env.NEXT_PUBLIC_AO_AUTH_MODE;
  } else {
    process.env.NEXT_PUBLIC_AO_AUTH_MODE = originalAuthEnvironment.public;
  }
  authkitMiddleware.mockClear();
  vi.resetModules();
});

describe("Cloud auth proxy", () => {
  it("does not initialize AuthKit for local auth", async () => {
    const proxy = await loadProxy({
      AO_CLOUD_AUTH_MODE: "local",
      NEXT_PUBLIC_AO_AUTH_MODE: "local",
    });

    expect(authkitMiddleware).not.toHaveBeenCalled();
    expect(proxy.default).toBeTypeOf("function");
  });

  it("initializes AuthKit for WorkOS auth", async () => {
    await loadProxy({
      AO_CLOUD_AUTH_MODE: "workos",
      NEXT_PUBLIC_AO_AUTH_MODE: "workos",
    });

    expect(authkitMiddleware).toHaveBeenCalledTimes(1);
  });
});
