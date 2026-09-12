import { createWorkOS, type User } from "@workos-inc/node";
import { app, dialog, ipcMain, safeStorage, shell } from "electron";
import {
  chmod,
  mkdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { createServer, type Server } from "node:http";
import path from "node:path";
import type { CloudAccount } from "../shared/cloud-account";
// Static import (not dynamic): the dynamic import created a static+dynamic
// import cycle with cloud-auth-local that made the bundler split chunks
// incorrectly and panic. Both modules only use each other's exports inside
// functions, so the static ES cycle is safe at module-init time.
import { revokeLocalSession } from "./cloud-auth-local";

// The WorkOS AuthKit client id is public configuration (it appears in every
// sign-in URL), so a baked default keeps sign-in working without build-time
// setup; VITE_WORKOS_CLIENT_ID overrides it per build when needed.
const DEFAULT_WORKOS_CLIENT_ID = "client_01KZ3VRKC374HS91XGRDPT3671";
const CLIENT_ID =
  import.meta.env.VITE_WORKOS_CLIENT_ID?.trim() ||
  (process.env.VITEST ? "client_test" : DEFAULT_WORKOS_CLIENT_ID);
// The packaged app receives the WorkOS callback through the ao-app:// deep link.
// A development build cannot: macOS routes ao-app:// to the installed app, not
// the unpackaged Electron binary. Dev builds therefore default to a loopback
// callback server (the standard desktop OAuth pattern) so sign-in works with
// zero setup; AO_CLOUD_AUTH_REDIRECT overrides either default.
const REDIRECT_URI =
  process.env.AO_CLOUD_AUTH_REDIRECT?.trim() ||
  (app.isPackaged ? "ao-app://callback" : "http://127.0.0.1:3000/callback");
const useLoopbackRedirect = /^https?:\/\//i.test(REDIRECT_URI);
const AUTH_STORE_FILE = "cloud-auth.bin";
const LEGACY_SESSION_FILE = "cloud-session.json";
const PKCE_TTL_MS = 10 * 60 * 1000;
const workos = CLIENT_ID ? createWorkOS({ clientId: CLIENT_ID }) : null;

// Set by installCloudIPC so the loopback callback server can push the new
// session to the renderer exactly like the deep-link path does.
let notifyRenderersFn: ((session: CloudAccount | null) => void) | null = null;
// At most one loopback callback server is armed at a time.
let loopbackServer: Server | null = null;

export interface StoredSession extends CloudAccount {
  accessToken: string;
  // WorkOS sessions carry a rotating refresh token; local (opaque-token)
  // sessions have none, so it is optional and absent for authProvider "local".
  refreshToken?: string;
  // Loopback control-plane base URL a local session was minted against, so
  // sign-out can revoke the opaque token server-side. Local sessions only.
  cpBaseUrl?: string;
}

export interface AuthStore {
  session: StoredSession | null;
  pkce: {
    codeVerifier: string;
    state: string;
    expiresAt: number;
  } | null;
}

/** A stored local (opaque-token) session, distinct from the WorkOS/JWT path. */
export function isLocalSession(
  session: StoredSession | null | undefined,
): session is StoredSession & { authProvider: "local" } {
  return session?.authProvider === "local";
}

const emptyStore = (): AuthStore => ({ session: null, pkce: null });
const memoryStores = new Map<string, AuthStore>();
const refreshes = new Map<string, Promise<CloudAccount | null>>();
const authGenerations = new Map<string, number>();
const authMutations = new Map<string, Promise<void>>();

function authGeneration(dataDir: string): number {
  return authGenerations.get(dataDir) ?? 0;
}

export function invalidateAuthOperations(dataDir: string): number {
  const generation = authGeneration(dataDir) + 1;
  authGenerations.set(dataDir, generation);
  return generation;
}

export async function withAuthMutation<T>(
  dataDir: string,
  mutation: () => Promise<T>,
): Promise<T> {
  const previous = authMutations.get(dataDir) ?? Promise.resolve();
  const result = previous.catch(() => undefined).then(mutation);
  const settled = result.then(
    () => undefined,
    () => undefined,
  );
  authMutations.set(dataDir, settled);
  try {
    return await result;
  } finally {
    if (authMutations.get(dataDir) === settled) {
      authMutations.delete(dataDir);
    }
  }
}

function storePath(dataDir: string): string {
  return path.join(dataDir, AUTH_STORE_FILE);
}

function protectedStorageAvailable(): boolean {
  if (!safeStorage.isEncryptionAvailable()) return false;
  if (process.platform !== "linux") return true;
  const backend = safeStorage.getSelectedStorageBackend();
  return backend !== "basic_text" && backend !== "unknown";
}

function encodeStore(store: AuthStore): Buffer {
  return safeStorage.encryptString(JSON.stringify(store));
}

function decodeStore(value: Buffer): AuthStore {
  return JSON.parse(safeStorage.decryptString(value)) as AuthStore;
}

export async function readAuthStore(dataDir: string): Promise<AuthStore> {
  const memoryStore = memoryStores.get(dataDir);
  if (memoryStore) return memoryStore;
  try {
    // A fresh profile has nothing to decrypt. Avoid initializing the OS
    // keychain on the startup path until an encrypted store actually exists.
    const value = await readFile(storePath(dataDir));
    if (!protectedStorageAvailable()) {
      await rm(storePath(dataDir), { force: true });
      return emptyStore();
    }
    return decodeStore(value);
  } catch {
    await rm(storePath(dataDir), { force: true });
    return emptyStore();
  }
}

export async function writeAuthStore(
  dataDir: string,
  store: AuthStore,
): Promise<void> {
  if (!protectedStorageAvailable()) {
    // A rotating refresh token must never fall back to plaintext persistence.
    // Linux's basic_text backend also reports encryption as available despite
    // using an unprotected hardcoded password, so keep that process-local too.
    memoryStores.set(dataDir, store);
    await rm(storePath(dataDir), { force: true });
    return;
  }
  memoryStores.delete(dataDir);
  await mkdir(dataDir, { recursive: true });
  const target = storePath(dataDir);
  await writeFile(target, encodeStore(store), { mode: 0o600 });
  await chmod(target, 0o600);
}

export async function removeAuthStore(dataDir: string): Promise<void> {
  memoryStores.delete(dataDir);
  await Promise.all([
    rm(storePath(dataDir), { force: true }),
    rm(path.join(dataDir, LEGACY_SESSION_FILE), { force: true }),
  ]);
}

function displayName(user: User): string {
  return (
    user.name?.trim() ||
    [user.firstName, user.lastName].filter(Boolean).join(" ") ||
    user.email
  );
}

function toStoredSession(
  accessToken: string,
  refreshToken: string,
  user: User,
): StoredSession {
  return {
    accessToken,
    refreshToken,
    authProvider: "workos",
    user: {
      id: user.id,
      email: user.email,
      displayName: displayName(user),
    },
    storedAt: new Date().toISOString(),
  };
}

export function publicAccount(session: StoredSession): CloudAccount {
  return {
    authProvider: session.authProvider,
    user: session.user,
    ...(session.organizations ? { organizations: session.organizations } : {}),
    storedAt: session.storedAt,
  };
}

function jwtPayload(token: string): Record<string, unknown> | null {
  try {
    const payload = token.split(".")[1];
    if (!payload) return null;
    return JSON.parse(
      Buffer.from(payload.replace(/-/g, "+").replace(/_/g, "/"), "base64").toString("utf8"),
    ) as Record<string, unknown>;
  } catch {
    return null;
  }
}

function tokenExpiresSoon(token: string): boolean {
  const exp = jwtPayload(token)?.exp;
  return typeof exp !== "number" || Date.now() >= exp * 1000 - 60_000;
}

function isTerminalRefreshFailure(error: unknown): boolean {
  if (!error || typeof error !== "object") {
    return String(error).toLowerCase().includes("refresh token already consumed");
  }
  const candidate = error as {
    code?: unknown;
    error?: unknown;
    message?: unknown;
    rawData?: { code?: unknown; error?: unknown; message?: unknown };
  };
  const details = [
    candidate.code,
    candidate.error,
    candidate.message,
    candidate.rawData?.code,
    candidate.rawData?.error,
    candidate.rawData?.message,
  ]
    .filter((value): value is string => typeof value === "string")
    .join(" ")
    .toLowerCase();
  return (
    details.includes("invalid_grant") ||
    details.includes("refresh token already consumed") ||
    details.includes("refresh token expired") ||
    details.includes("refresh token revoked")
  );
}

export async function getCloudSession(
  dataDir: string,
): Promise<CloudAccount | null> {
  const store = await readAuthStore(dataDir);
  // Local (opaque-token) sessions never refresh and do not require WorkOS to be
  // configured; return the stored identity directly.
  if (isLocalSession(store.session)) return publicAccount(store.session);
  if (!workos) return null;
  const activeRefresh = refreshes.get(dataDir);
  if (activeRefresh) return activeRefresh;
  if (!store.session) return null;
  if (!tokenExpiresSoon(store.session.accessToken)) {
    return publicAccount(store.session);
  }

  const pendingRefresh = refreshes.get(dataDir);
  if (pendingRefresh) return pendingRefresh;
  const refresh = refreshCloudSession(
    dataDir,
    store.session,
    authGeneration(dataDir),
  );
  refreshes.set(dataDir, refresh);
  try {
    return await refresh;
  } finally {
    if (refreshes.get(dataDir) === refresh) refreshes.delete(dataDir);
  }
}

/**
 * Fast, non-blocking variant of getCloudSession for the renderer's initial
 * mount. Returns the on-disk identity immediately (no WorkOS round-trip), so
 * the sidebar and the create-project Cloud tab render right away instead of
 * blanking for seconds while a token refresh runs on cold open. If the stored
 * token is stale, the refresh runs in the background and the corrected result
 * (rotated session, or null on terminal failure) is pushed over
 * cloud:sessionChanged, which the renderer already subscribes to. The CP proxy
 * refreshes independently via getCloudAccessToken, so outbound requests stay
 * authorized regardless.
 */
export async function getCloudSessionCached(
  dataDir: string,
): Promise<CloudAccount | null> {
  const store = await readAuthStore(dataDir);
  // Local sessions carry no rotating token — return the stored identity as-is.
  if (isLocalSession(store.session)) return publicAccount(store.session);
  if (!workos) return null;
  if (!store.session) return null;
  if (tokenExpiresSoon(store.session.accessToken)) {
    void getCloudSession(dataDir)
      .then((refreshed) => notifyRenderersFn?.(refreshed))
      .catch(() => {});
  }
  return publicAccount(store.session);
}

/**
 * Raw bearer token for the main-process control-plane proxy
 * (cloud-cp-proxy.ts). Runs the same refresh-if-expiring path as
 * getCloudSession, then reads the (possibly rotated) token back out of the
 * store. Main-process only: the token must never be sent to a renderer or
 * exposed over IPC.
 */
export async function getCloudAccessToken(
  dataDir: string,
): Promise<string | null> {
  const store = await readAuthStore(dataDir);
  // The local opaque token is returned verbatim (no JWT refresh path).
  if (isLocalSession(store.session)) return store.session.accessToken;
  const account = await getCloudSession(dataDir);
  if (account === null) return null;
  const refreshed = await readAuthStore(dataDir);
  return refreshed.session?.accessToken ?? null;
}

async function refreshCloudSession(
  dataDir: string,
  storedSession: StoredSession,
  generation: number,
): Promise<CloudAccount | null> {
  // Only WorkOS sessions carry a refresh token and reach this path; a session
  // without one cannot be rotated, so surface it unchanged.
  if (!storedSession.refreshToken) return publicAccount(storedSession);
  try {
    const refreshed = await workos!.userManagement.authenticateWithRefreshToken({
      clientId: CLIENT_ID,
      refreshToken: storedSession.refreshToken,
    });
    const session = toStoredSession(
      refreshed.accessToken,
      refreshed.refreshToken,
      refreshed.user,
    );
    return withAuthMutation(dataDir, async () => {
      if (authGeneration(dataDir) !== generation) return null;
      const currentStore = await readAuthStore(dataDir);
      if (
        currentStore.session?.refreshToken !== storedSession.refreshToken
      ) {
        return null;
      }
      await writeAuthStore(dataDir, { ...currentStore, session });
      return publicAccount(session);
    });
  } catch (error) {
    if (!isTerminalRefreshFailure(error)) {
      return publicAccount(storedSession);
    }
    await withAuthMutation(dataDir, async () => {
      if (authGeneration(dataDir) !== generation) return;
      const currentStore = await readAuthStore(dataDir);
      if (
        currentStore.session?.refreshToken === storedSession.refreshToken
      ) {
        await removeAuthStore(dataDir);
      }
    });
    return null;
  }
}

export async function beginCloudSignIn(dataDir: string): Promise<void> {
  if (!workos) {
    throw new Error("WorkOS is not configured.");
  }
  const { url, state, codeVerifier } =
    await workos.userManagement.getAuthorizationUrlWithPKCE({
      clientId: CLIENT_ID,
      provider: "authkit",
      prompt: "login",
      maxAge: 0,
      redirectUri: REDIRECT_URI,
    });
  invalidateAuthOperations(dataDir);
  const store = await readAuthStore(dataDir);
  await writeAuthStore(dataDir, {
    ...store,
    pkce: {
      codeVerifier,
      state,
      expiresAt: Date.now() + PKCE_TTL_MS,
    },
  });
  if (useLoopbackRedirect) {
    startLoopbackCallbackServer(dataDir);
  }
  await shell.openExternal(url);
}

// completeCloudSignIn validates the pending PKCE state and exchanges the code
// for a session. Shared by the deep-link path and the loopback callback server.
async function completeCloudSignIn(
  code: string | null,
  callbackState: string | null,
  dataDir: string,
): Promise<CloudAccount> {
  if (!workos) throw new Error("WorkOS is not configured.");
  if (!code || !callbackState) throw new Error("WorkOS callback is incomplete.");

  const store = await readAuthStore(dataDir);
  if (!store.pkce) throw new Error("No WorkOS sign-in is pending.");
  if (store.pkce.expiresAt < Date.now()) {
    await writeAuthStore(dataDir, { ...store, pkce: null });
    throw new Error("The WorkOS sign-in request expired.");
  }
  if (callbackState !== store.pkce.state) {
    throw new Error("WorkOS callback state did not match.");
  }

  const result = await workos.userManagement.authenticateWithCode({
    clientId: CLIENT_ID,
    code,
    codeVerifier: store.pkce.codeVerifier,
  });
  const session = toStoredSession(
    result.accessToken,
    result.refreshToken,
    result.user,
  );
  await writeAuthStore(dataDir, { session, pkce: null });
  return publicAccount(session);
}

export async function handleCloudDeepLink(
  rawURL: string,
  dataDir: string,
): Promise<CloudAccount | null> {
  if (!workos) throw new Error("WorkOS is not configured.");
  const url = new URL(rawURL);
  if (url.protocol !== "ao-app:" || url.hostname !== "callback") return null;
  const error = url.searchParams.get("error");
  if (error) {
    throw new Error(
      url.searchParams.get("error_description") || `WorkOS sign-in failed: ${error}`,
    );
  }
  return completeCloudSignIn(
    url.searchParams.get("code"),
    url.searchParams.get("state"),
    dataDir,
  );
}

const CALLBACK_HTML = (title: string, body: string): string =>
  `<!doctype html><meta charset="utf-8"><title>${title}</title>` +
  `<body style="font:15px -apple-system,system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1.5rem;color:#111">` +
  `<h1 style="font-size:1.25rem">${title}</h1><p style="color:#555">${body}</p></body>`;

function closeLoopbackServer(): void {
  if (loopbackServer) {
    loopbackServer.close();
    loopbackServer = null;
  }
}

// startLoopbackCallbackServer arms a one-shot local HTTP server that receives
// the WorkOS redirect for a development build, exchanges the code, and hands the
// session to the renderer. It closes itself after the first callback or the PKCE
// window, whichever comes first.
function startLoopbackCallbackServer(dataDir: string): void {
  const redirect = new URL(REDIRECT_URI);
  closeLoopbackServer();
  const server = createServer((req, res) => {
    const requestURL = new URL(req.url ?? "/", `http://${redirect.host}`);
    if (requestURL.pathname !== redirect.pathname) {
      res.writeHead(404, { "Content-Type": "text/plain" });
      res.end("Not found");
      return;
    }
    void (async () => {
      try {
        const error = requestURL.searchParams.get("error");
        if (error) {
          throw new Error(
            requestURL.searchParams.get("error_description") ||
              `WorkOS sign-in failed: ${error}`,
          );
        }
        const account = await completeCloudSignIn(
          requestURL.searchParams.get("code"),
          requestURL.searchParams.get("state"),
          dataDir,
        );
        notifyRenderersFn?.(account);
        res.writeHead(200, { "Content-Type": "text/html" });
        res.end(
          CALLBACK_HTML(
            "Signed in to AO Cloud",
            "You can close this tab and return to Agent Orchestrator.",
          ),
        );
      } catch (callbackError) {
        console.error("WorkOS loopback callback failed:", callbackError);
        res.writeHead(400, { "Content-Type": "text/html" });
        res.end(
          CALLBACK_HTML(
            "Sign-in failed",
            "Return to Agent Orchestrator and try signing in again.",
          ),
        );
      } finally {
        closeLoopbackServer();
      }
    })();
  });
  server.on("error", (error) => {
    console.error("WorkOS loopback callback server error:", error);
    closeLoopbackServer();
  });
  server.listen(Number(redirect.port) || 80, redirect.hostname);
  loopbackServer = server;
  const timer = setTimeout(closeLoopbackServer, PKCE_TTL_MS);
  timer.unref?.();
}

export async function signOutCloud(dataDir: string): Promise<void> {
  invalidateAuthOperations(dataDir);
  await withAuthMutation(dataDir, () => removeAuthStore(dataDir));
}

function signInFailureDetail(error: unknown): string {
  const message = error instanceof Error ? error.message.toLowerCase() : "";
  if (message.includes("cancel") || message.includes("access_denied")) {
    return "The WorkOS sign-in was cancelled. You can try again whenever you are ready.";
  }
  if (message.includes("expired")) {
    return "The WorkOS sign-in request expired. Start sign-in again to continue.";
  }
  if (
    message.includes("state") ||
    message.includes("pending") ||
    message.includes("incomplete")
  ) {
    return "The WorkOS response could not be verified. Start sign-in again to continue.";
  }
  if (message.includes("network") || message.includes("fetch")) {
    return "Agent Orchestrator could not reach WorkOS. Check your connection and try again.";
  }
  return "Agent Orchestrator could not complete WorkOS sign-in. Please try again.";
}

export async function showCloudSignInFailure(error: unknown): Promise<void> {
  await dialog.showMessageBox({
    type: "error",
    title: "AO Cloud sign-in failed",
    message: "Unable to sign in to AO Cloud",
    detail: signInFailureDetail(error),
  });
}

export function registerCloudProtocol(): void {
  if (process.defaultApp && process.argv.length >= 2) {
    app.setAsDefaultProtocolClient("ao-app", process.execPath, [
      path.resolve(process.argv[1]),
    ]);
    return;
  }
  app.setAsDefaultProtocolClient("ao-app");
}

export function installCloudIPC(
  getDataDir: () => string,
  notifyRenderers: (session: CloudAccount | null) => void,
): void {
  notifyRenderersFn = notifyRenderers;
  ipcMain.handle("cloud:getSession", () => getCloudSessionCached(getDataDir()));
  ipcMain.handle("cloud:signIn", async () => {
    if (!workos) {
      await dialog.showMessageBox({
        type: "info",
        title: "WorkOS not configured",
        message: "WorkOS sign-in is not configured.",
        detail:
          "Set VITE_WORKOS_CLIENT_ID and restart Agent Orchestrator to enable sign-in.",
      });
      return;
    }
    await beginCloudSignIn(getDataDir());
  });
  ipcMain.handle("cloud:signOut", async () => {
    const dataDir = getDataDir();
    // A local (opaque-token) session is revoked server-side first, best-effort;
    // WorkOS sessions skip this. Store removal is uniform via signOutCloud.
    try {
      const store = await readAuthStore(dataDir);
      if (isLocalSession(store.session)) {
        await revokeLocalSession(store.session);
      }
    } catch {
      // Sign-out must never fail on a revoke round-trip; clear the store anyway.
    }
    await signOutCloud(dataDir);
    notifyRenderers(null);
  });
}
