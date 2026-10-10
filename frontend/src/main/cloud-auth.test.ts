import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const ACCESS_TOKEN = `header.${Buffer.from(
  JSON.stringify({ exp: 4_102_444_800, sid: "session_123" }),
).toString("base64url")}.signature`;
const EXPIRED_ACCESS_TOKEN = `header.${Buffer.from(
  JSON.stringify({ exp: 1, sid: "session_123" }),
).toString("base64url")}.signature`;

const mocks = vi.hoisted(() => ({
  authenticateWithCode: vi.fn(),
  authenticateWithRefreshToken: vi.fn(),
  decryptString: vi.fn((value: Buffer) => value.toString("utf8")),
  encryptString: vi.fn((value: string) => Buffer.from(value, "utf8")),
  encryptionAvailable: true,
  selectedStorageBackend: "gnome_libsecret",
  getAuthorizationUrlWithPKCE: vi.fn(),
  ipcHandle: vi.fn(),
  notifyRenderers: vi.fn(),
  openExternal: vi.fn(),
  showMessageBox: vi.fn(),
  providerAuthenticate: vi.fn(),
}));

// The browser login itself is exercised in provider-auth-flow tests; here it
// just returns a captured credential.
vi.mock("./provider-auth-flow", () => ({
  providerAuthFlow: () => ({ authenticate: mocks.providerAuthenticate }),
}));

vi.mock("@workos-inc/node", () => ({
  createWorkOS: () => ({
    userManagement: {
      authenticateWithCode: mocks.authenticateWithCode,
      authenticateWithRefreshToken: mocks.authenticateWithRefreshToken,
      getAuthorizationUrlWithPKCE: mocks.getAuthorizationUrlWithPKCE,
    },
  }),
}));

vi.mock("electron", () => ({
  app: {
    setAsDefaultProtocolClient: vi.fn(),
    // These tests exercise the packaged deep-link (ao-app://) sign-in flow;
    // unpackaged builds default to a loopback redirect instead.
    isPackaged: true,
  },
  dialog: { showMessageBox: mocks.showMessageBox },
  ipcMain: { handle: mocks.ipcHandle },
  safeStorage: {
    decryptString: mocks.decryptString,
    encryptString: mocks.encryptString,
    getSelectedStorageBackend: () => mocks.selectedStorageBackend,
    isEncryptionAvailable: () => mocks.encryptionAvailable,
  },
  shell: { openExternal: mocks.openExternal },
}));

import {
  beginCloudSignIn,
  getCloudAccessToken,
  getCloudSession,
  handleCloudDeepLink,
  installCloudIPC,
  readAuthStore,
  resolveCloudDataDir,
  showCloudSignInFailure,
  signOutCloud,
  writeAuthStore,
} from "./cloud-auth";
import { resolveDesktopDataDir } from "./telemetry-policy-file";

describe("cloud storage isolation", () => {
  let homeDir: string;
  let sharedDir: string;
  const sentinels = ["cloud-auth.bin", "cloud-session.json"];

  beforeEach(async () => {
    vi.clearAllMocks();
    mocks.encryptionAvailable = true;
    mocks.selectedStorageBackend = "gnome_libsecret";
    homeDir = await mkdtemp(path.join(os.tmpdir(), "ao-cloud-isolation-"));
    sharedDir = path.join(homeDir, ".ao", "dev");
    await mkdir(sharedDir, { recursive: true });
    for (const name of sentinels) {
      await writeFile(path.join(sharedDir, name), `protected-${name}`);
    }
  });

  afterEach(async () => {
    vi.restoreAllMocks();
    await rm(homeDir, { recursive: true, force: true });
  });

  const instanceDir = (instance: string) => {
    const env = { AO_DATA_DIR: path.join(homeDir, ".ao", "dev", instance, "data") };
    const desktopDir = resolveDesktopDataDir(env, homeDir, "/checkout", false);
    return resolveCloudDataDir(env, homeDir, desktopDir, true);
  };

  const getSession = (dataDir: string) => {
    installCloudIPC(() => dataDir, mocks.notifyRenderers);
    const handler = mocks.ipcHandle.mock.calls.find(
      ([channel]) => channel === "cloud:getSession",
    )?.[1] as () => Promise<unknown>;
    return handler();
  };

  const expectSharedUnchanged = async () => {
    for (const name of sentinels) {
      await expect(readFile(path.join(sharedDir, name), "utf8")).resolves.toBe(`protected-${name}`);
    }
  };

  it.each([true, false])("preserves default cloud storage with isDev=%s", (isDev) => {
    for (const value of [undefined, "", "   "]) {
      const env = { AO_DATA_DIR: value };
      const desktopDir = resolveDesktopDataDir(env, homeDir, "/checkout", !isDev);
      expect(resolveCloudDataDir(env, homeDir, desktopDir, isDev)).toBe(
        isDev ? sharedDir : path.join(homeDir, ".ao"),
      );
    }
  });

  it.each([true, false])("uses the resolved override with isDev=%s", (isDev) => {
    for (const value of [path.join(homeDir, "isolated"), " relative-data "]) {
      const env = { AO_DATA_DIR: value };
      const desktopDir = resolveDesktopDataDir(env, homeDir, "/launch-cwd", !isDev);
      expect(resolveCloudDataDir(env, homeDir, desktopDir, isDev)).toBe(
        path.resolve("/launch-cwd", value.trim()),
      );
    }
  });

  it.each(["agentic-supervisor", "agentic-target"])("keeps empty %s startup out of shared auth", async (instance) => {
    await expect(getSession(instanceDir(instance))).resolves.toBeNull();
    expect(mocks.decryptString).not.toHaveBeenCalled();
    await expectSharedUnchanged();
  });

  it.each([true, false])("confines startup removal with encryption available=%s", async (available) => {
    const dataDir = instanceDir("agentic-target");
    await mkdir(dataDir, { recursive: true });
    await writeFile(path.join(dataDir, "cloud-auth.bin"), "invalid-instance-store");
    mocks.encryptionAvailable = available;
    await expect(getSession(dataDir)).resolves.toBeNull();
    await expect(readFile(path.join(dataDir, "cloud-auth.bin"))).rejects.toMatchObject({ code: "ENOENT" });
    await expectSharedUnchanged();
  });

  it("confines refresh writes and sign-out removal to the instance", async () => {
    const dataDir = instanceDir("agentic-supervisor");
    await writeAuthStore(dataDir, {
      session: {
        authProvider: "workos",
        accessToken: EXPIRED_ACCESS_TOKEN,
        refreshToken: "instance-refresh",
        user: { id: "instance-user", email: "instance@example.test", displayName: "Instance" },
        storedAt: "2026-10-06T00:00:00Z",
      },
      pkce: null,
    });
    mocks.authenticateWithRefreshToken.mockResolvedValueOnce({
      accessToken: ACCESS_TOKEN,
      refreshToken: "instance-refreshed",
      user: { id: "instance-user", email: "instance@example.test", name: "Instance" },
    });
    const refreshed = new Promise<void>((resolve) => {
      mocks.notifyRenderers.mockImplementationOnce(() => resolve());
    });
    await getSession(dataDir);
    await refreshed;
    expect(mocks.authenticateWithRefreshToken).toHaveBeenCalledOnce();
    await expect(readAuthStore(dataDir)).resolves.toMatchObject({ session: { refreshToken: "instance-refreshed" } });
    await expectSharedUnchanged();
    await writeFile(path.join(dataDir, "cloud-session.json"), "instance-legacy");
    await signOutCloud(dataDir);
    for (const name of sentinels) {
      await expect(readFile(path.join(dataDir, name))).rejects.toMatchObject({ code: "ENOENT" });
    }
    await expectSharedUnchanged();
  });
});

describe("native WorkOS authentication", () => {
  let dataDir: string;

  beforeEach(async () => {
    vi.clearAllMocks();
    mocks.encryptionAvailable = true;
    mocks.selectedStorageBackend = "gnome_libsecret";
    dataDir = await mkdtemp(path.join(os.tmpdir(), "ao-cloud-auth-"));
    installCloudIPC(() => dataDir, mocks.notifyRenderers);
    mocks.getAuthorizationUrlWithPKCE.mockResolvedValue({
      url: "https://workos.example/authorize",
      state: "state_123",
      codeVerifier: "verifier_123",
    });
    mocks.authenticateWithCode.mockResolvedValue({
      accessToken: ACCESS_TOKEN,
      refreshToken: "refresh_123",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
        firstName: "Person",
        lastName: "Example",
      },
    });
  });

  afterEach(async () => {
    vi.restoreAllMocks();
    await rm(dataDir, { recursive: true, force: true });
  });

  it("starts PKCE and exchanges the callback without an AO website", async () => {
    await beginCloudSignIn(dataDir);
    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://workos.example/authorize",
    );
    expect(mocks.getAuthorizationUrlWithPKCE).toHaveBeenCalledWith(
      expect.objectContaining({
        provider: "authkit",
        prompt: "login",
        maxAge: 0,
        redirectUri: "https://api.aoagents.dev/app/auth/return",
      }),
    );

    const session = await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );
    expect(session).toMatchObject({
      authProvider: "workos",
      user: {
        id: "user_123",
        email: "person@example.com",
        displayName: "Person Example",
      },
    });
    expect(session).not.toHaveProperty("accessToken");
    expect(session).not.toHaveProperty("refreshToken");
    await expect(getCloudSession(dataDir)).resolves.toMatchObject({
      user: { email: "person@example.com" },
    });
  });

  it("silently accepts the same completed callback without exchanging its code again", async () => {
    await beginCloudSignIn(dataDir);
    const callback = "ao-app://callback?code=code_123&state=state_123";
    const account = await handleCloudDeepLink(callback, dataDir);

    await expect(handleCloudDeepLink(callback, dataDir)).resolves.toEqual(account);
    expect(mocks.authenticateWithCode).toHaveBeenCalledTimes(1);
    expect(mocks.showMessageBox).not.toHaveBeenCalled();
  });

  it("exchanges a callback once when the OS delivers it concurrently", async () => {
    await beginCloudSignIn(dataDir);
    const callback = "ao-app://callback?code=code_123&state=state_123";
    const accounts = await Promise.all([
      handleCloudDeepLink(callback, dataDir),
      handleCloudDeepLink(callback, dataDir),
    ]);

    expect(accounts[0]).toEqual(accounts[1]);
    expect(mocks.authenticateWithCode).toHaveBeenCalledTimes(1);
  });

  it("ignores callbacks with no login pending without changing the signed-in account", async () => {
    await beginCloudSignIn(dataDir);
    const account = await handleCloudDeepLink("ao-app://callback?code=code_123&state=state_123", dataDir);
    await expect(handleCloudDeepLink(
      "ao-app://callback?code=different_code&state=state_123", dataDir,
    )).resolves.toEqual(account);
    await expect(handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=unverified_state", dataDir,
    )).resolves.toEqual(account);
    expect(mocks.authenticateWithCode).toHaveBeenCalledTimes(1);
  });

  it("quietly returns to an account saved before callback deduplication was added", async () => {
    await beginCloudSignIn(dataDir);
    const callback = "ao-app://callback?code=code_123&state=state_123";
    const account = await handleCloudDeepLink(callback, dataDir);
    const store = await readAuthStore(dataDir);
    delete store.completedCallback;
    await writeAuthStore(dataDir, store);

    await expect(handleCloudDeepLink(callback, dataDir)).resolves.toEqual(account);
    expect(mocks.authenticateWithCode).toHaveBeenCalledTimes(1);
  });

  it("does not restore a signed-out account when its old return button is clicked", async () => {
    await beginCloudSignIn(dataDir);
    const callback = "ao-app://callback?code=code_123&state=state_123";
    await handleCloudDeepLink(callback, dataDir);
    await signOutCloud(dataDir);

    await expect(handleCloudDeepLink(callback, dataDir)).rejects.toThrow("No WorkOS sign-in is pending");
    await expect(getCloudSession(dataDir)).resolves.toBeNull();
    expect(mocks.authenticateWithCode).toHaveBeenCalledTimes(1);
  });

  it("keeps a newer pending login when an already completed callback repeats", async () => {
    await beginCloudSignIn(dataDir);
    const callback = "ao-app://callback?code=code_123&state=state_123";
    const account = await handleCloudDeepLink(callback, dataDir);
    mocks.getAuthorizationUrlWithPKCE.mockResolvedValueOnce({
      url: "https://workos.example/authorize", state: "state_456", codeVerifier: "verifier_456",
    });
    await beginCloudSignIn(dataDir);

    await expect(handleCloudDeepLink(callback, dataDir)).resolves.toEqual(account);
    await handleCloudDeepLink("ao-app://callback?code=code_456&state=state_456", dataDir);
    expect(mocks.authenticateWithCode).toHaveBeenCalledTimes(2);
    expect(mocks.authenticateWithCode).toHaveBeenLastCalledWith(expect.objectContaining({
      code: "code_456", codeVerifier: "verifier_456",
    }));
  });

  it("requires an AO Cloud session before starting provider login", async () => {
    const handler = mocks.ipcHandle.mock.calls.find(
      ([channel]) => channel === "cloud:connectProviderAuth",
    )?.[1] as ((event: unknown, input: unknown) => Promise<void>) | undefined;
    expect(handler).toBeTypeOf("function");
    await expect(
      handler?.({}, {
        baseUrl: "https://cloud.example",
        provider: "codex",
      }),
    ).rejects.toThrow("Sign in to AO Cloud before connecting a provider.");
  });

  describe("Claude browser login local fallback token", () => {
    const tokenFile = () => path.join(dataDir, "harnesses", "claude-code", "oauth-token");
    const connect = async () => {
      const handler = mocks.ipcHandle.mock.calls.find(
        ([channel]) => channel === "cloud:connectProviderAuth",
      )?.[1] as (event: unknown, input: unknown) => Promise<void>;
      return handler({}, { baseUrl: "https://cloud.example", provider: "claude-code", persistLocalClaudeToken: true });
    };

    beforeEach(async () => {
      await beginCloudSignIn(dataDir);
      await handleCloudDeepLink("ao-app://callback?code=code_123&state=state_123", dataDir);
      mocks.providerAuthenticate.mockResolvedValue({
        provider: "claude-code",
        credentialType: "oauth_token",
        secret: "sk-ant-oat01-test-token",
      });
    });

    it("keeps the local token once the cloud credential is saved", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("{}", { status: 200 }));
      await connect();
      await expect(readFile(tokenFile(), "utf8")).resolves.toBe("sk-ant-oat01-test-token");
    });

    it("does not leave a local token behind when the cloud save fails", async () => {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("{}", { status: 500 }));
      await expect(connect()).rejects.toThrow("AO Cloud could not save the provider credential.");
      await expect(readFile(tokenFile(), "utf8")).rejects.toMatchObject({ code: "ENOENT" });
    });

    it("does not leave a local token behind when the cloud is unreachable", async () => {
      vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("fetch failed"));
      await expect(connect()).rejects.toThrow("fetch failed");
      await expect(readFile(tokenFile(), "utf8")).rejects.toMatchObject({ code: "ENOENT" });
    });
  });

  it("rejects callbacks whose OAuth state does not match", async () => {
    await beginCloudSignIn(dataDir);
    await expect(
      handleCloudDeepLink(
        "ao-app://callback?code=code_123&state=attacker_state",
        dataDir,
      ),
    ).rejects.toThrow("state did not match");
  });

  it("keeps the auth store in memory when OS encryption is unavailable", async () => {
    mocks.encryptionAvailable = false;
    await beginCloudSignIn(dataDir);
    const account = await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );

    expect(account?.user.email).toBe("person@example.com");
    await expect(
      readFile(path.join(dataDir, "cloud-auth.bin")),
    ).rejects.toMatchObject({ code: "ENOENT" });
    await expect(getCloudSession(dataDir)).resolves.toMatchObject({
      user: { email: "person@example.com" },
    });
    await signOutCloud(dataDir);
  });

  it("keeps the auth store in memory with Linux basic_text storage", async () => {
    vi.spyOn(process, "platform", "get").mockReturnValue("linux");
    mocks.selectedStorageBackend = "basic_text";
    await beginCloudSignIn(dataDir);
    const account = await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );

    expect(account?.user.email).toBe("person@example.com");
    await expect(
      readFile(path.join(dataDir, "cloud-auth.bin")),
    ).rejects.toMatchObject({ code: "ENOENT" });
    await expect(getCloudSession(dataDir)).resolves.toMatchObject({
      user: { email: "person@example.com" },
    });
    await signOutCloud(dataDir);
  });

  it("shows a bounded error when the callback cannot be completed", async () => {
    await showCloudSignInFailure(
      new Error("The WorkOS sign-in request expired with secret details"),
    );

    expect(mocks.showMessageBox).toHaveBeenCalledWith({
      type: "error",
      title: "AO Cloud sign-in failed",
      message: "Unable to sign in to AO Cloud",
      detail:
        "The WorkOS sign-in request expired. Start sign-in again to continue.",
    });
  });

  it("shares one rotating-token refresh between concurrent callers", async () => {
    mocks.authenticateWithCode.mockResolvedValueOnce({
      accessToken: EXPIRED_ACCESS_TOKEN,
      refreshToken: "refresh_123",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
      },
    });
    let resolveRefresh:
      | ((value: {
          accessToken: string;
          refreshToken: string;
          user: {
            id: string;
            email: string;
            name: string;
          };
        }) => void)
      | undefined;
    mocks.authenticateWithRefreshToken.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveRefresh = resolve;
      }),
    );
    await beginCloudSignIn(dataDir);
    await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );

    const first = getCloudSession(dataDir);
    const second = getCloudSession(dataDir);
    await vi.waitFor(() =>
      expect(mocks.authenticateWithRefreshToken).toHaveBeenCalledOnce(),
    );
    resolveRefresh?.({
      accessToken: ACCESS_TOKEN,
      refreshToken: "refresh_456",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
      },
    });

    const [firstAccount, secondAccount] = await Promise.all([first, second]);
    expect(firstAccount).toEqual(secondAccount);
    expect(firstAccount).not.toHaveProperty("accessToken");
    expect(mocks.authenticateWithRefreshToken).toHaveBeenCalledOnce();
  });

  it("clears an expired session when token refresh fails", async () => {
    mocks.authenticateWithCode.mockResolvedValueOnce({
      accessToken: EXPIRED_ACCESS_TOKEN,
      refreshToken: "refresh_123",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
      },
    });
    mocks.authenticateWithRefreshToken.mockRejectedValueOnce(
      new Error("refresh token already consumed"),
    );
    await beginCloudSignIn(dataDir);
    await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );

    await expect(getCloudSession(dataDir)).resolves.toBeNull();
    await expect(getCloudSession(dataDir)).resolves.toBeNull();
    expect(mocks.authenticateWithRefreshToken).toHaveBeenCalledOnce();
    expect(mocks.notifyRenderers).toHaveBeenCalledWith(null);
  });

  it("publishes signed-out state when a token request finds the auth store missing", async () => {
    await beginCloudSignIn(dataDir);
    await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );
    await rm(path.join(dataDir, "cloud-auth.bin"), { force: true });

    await expect(getCloudAccessToken(dataDir)).resolves.toBeNull();
    expect(mocks.notifyRenderers).toHaveBeenCalledWith(null);
  });

  it("preserves encrypted credentials after a retryable refresh failure", async () => {
    mocks.authenticateWithCode.mockResolvedValueOnce({
      accessToken: EXPIRED_ACCESS_TOKEN,
      refreshToken: "refresh_123",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
      },
    });
    mocks.authenticateWithRefreshToken
      .mockRejectedValueOnce(
        Object.assign(new Error("WorkOS temporarily unavailable"), {
          status: 503,
        }),
      )
      .mockResolvedValueOnce({
        accessToken: ACCESS_TOKEN,
        refreshToken: "refresh_456",
        user: {
          id: "user_123",
          email: "person@example.com",
          name: "Person Example",
        },
      });
    await beginCloudSignIn(dataDir);
    await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );

    await expect(getCloudSession(dataDir)).resolves.toMatchObject({
      user: { email: "person@example.com" },
    });
    await expect(
      readFile(path.join(dataDir, "cloud-auth.bin")),
    ).resolves.toBeInstanceOf(Buffer);
    await expect(getCloudSession(dataDir)).resolves.toMatchObject({
      user: { email: "person@example.com" },
    });
    expect(mocks.authenticateWithRefreshToken).toHaveBeenCalledTimes(2);
  });

  it("does not restore a refreshed session after explicit sign-out", async () => {
    mocks.authenticateWithCode.mockResolvedValueOnce({
      accessToken: EXPIRED_ACCESS_TOKEN,
      refreshToken: "refresh_123",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
      },
    });
    let resolveRefresh:
      | ((value: {
          accessToken: string;
          refreshToken: string;
          user: {
            id: string;
            email: string;
            name: string;
          };
        }) => void)
      | undefined;
    mocks.authenticateWithRefreshToken.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveRefresh = resolve;
      }),
    );
    await beginCloudSignIn(dataDir);
    await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );

    const pendingSession = getCloudSession(dataDir);
    await vi.waitFor(() =>
      expect(mocks.authenticateWithRefreshToken).toHaveBeenCalledOnce(),
    );
    await signOutCloud(dataDir);
    resolveRefresh?.({
      accessToken: ACCESS_TOKEN,
      refreshToken: "refresh_456",
      user: {
        id: "user_123",
        email: "person@example.com",
        name: "Person Example",
      },
    });

    await expect(pendingSession).resolves.toBeNull();
    await expect(getCloudSession(dataDir)).resolves.toBeNull();
    await expect(
      readFile(path.join(dataDir, "cloud-auth.bin")),
    ).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("signs out locally without opening the browser", async () => {
    await beginCloudSignIn(dataDir);
    await handleCloudDeepLink(
      "ao-app://callback?code=code_123&state=state_123",
      dataDir,
    );
    mocks.openExternal.mockClear();

    await signOutCloud(dataDir);

    expect(mocks.openExternal).not.toHaveBeenCalled();
    await expect(getCloudSession(dataDir)).resolves.toBeNull();
  });
});
