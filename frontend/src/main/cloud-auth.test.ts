import { mkdtemp, readFile, rm } from "node:fs/promises";
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
  openExternal: vi.fn(),
  showMessageBox: vi.fn(),
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
  ipcMain: { handle: vi.fn() },
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
  getCloudSession,
  handleCloudDeepLink,
  showCloudSignInFailure,
  signOutCloud,
} from "./cloud-auth";

describe("native WorkOS authentication", () => {
  let dataDir: string;

  beforeEach(async () => {
    vi.clearAllMocks();
    mocks.encryptionAvailable = true;
    mocks.selectedStorageBackend = "gnome_libsecret";
    dataDir = await mkdtemp(path.join(os.tmpdir(), "ao-cloud-auth-"));
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
        redirectUri: "ao-app://callback",
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
