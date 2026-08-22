import { app, dialog, ipcMain, safeStorage, shell } from "electron";
import { createHash, randomBytes } from "node:crypto";
import { chmod, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";
import type { AddressInfo } from "node:net";
import type { CloudAccount } from "../shared/cloud-account";

const CLOUD_API_URL =
	import.meta.env.VITE_AO_CLOUD_API_URL?.trim().replace(/\/+$/, "") ||
	process.env.AO_CLOUD_API_URL?.trim().replace(/\/+$/, "") ||
	(process.env.VITEST ? "https://cloud.example" : "");
const GOOGLE_CLIENT_ID =
	import.meta.env.VITE_AO_CLOUD_GOOGLE_CLIENT_ID?.trim() ||
	process.env.AO_CLOUD_GOOGLE_CLIENT_ID?.trim() ||
	(process.env.VITEST ? "client_test" : "");
const GOOGLE_CLIENT_SECRET = process.env.AO_CLOUD_GOOGLE_CLIENT_SECRET?.trim() || "";
const AUTH_STORE_FILE = "cloud-auth.bin";
const LEGACY_SESSION_FILE = "cloud-session.json";
const PKCE_TTL_MS = 10 * 60 * 1000;

interface StoredSession extends CloudAccount {
	accessToken: string;
	refreshToken: string;
	expiresAt: string;
}

interface AuthStore {
	session: StoredSession | null;
	pkce: { codeVerifier: string; state: string; expiresAt: number } | null;
}

interface AuthResponse {
	accessToken: string;
	refreshToken: string;
	expiresAt: string;
	user: CloudAccount["user"] & { authProvider?: string };
	organizations: CloudAccount["organizations"];
}

const emptyStore = (): AuthStore => ({ session: null, pkce: null });
const memoryStores = new Map<string, AuthStore>();
const refreshes = new Map<string, Promise<CloudAccount | null>>();
const authGenerations = new Map<string, number>();
const authMutations = new Map<string, Promise<void>>();

export function cloudAuthConfigured(): boolean {
	return Boolean(CLOUD_API_URL && GOOGLE_CLIENT_ID);
}

function authGeneration(dataDir: string): number {
	return authGenerations.get(dataDir) ?? 0;
}

function invalidateAuthOperations(dataDir: string): number {
	const generation = authGeneration(dataDir) + 1;
	authGenerations.set(dataDir, generation);
	return generation;
}

async function withAuthMutation<T>(dataDir: string, mutation: () => Promise<T>): Promise<T> {
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
		if (authMutations.get(dataDir) === settled) authMutations.delete(dataDir);
	}
}

function storePath(dataDir: string): string {
	return path.join(dataDir, AUTH_STORE_FILE);
}

function protectedStorageAvailable(): boolean {
	// Local development can opt into process-only credentials so an unsigned
	// Electron build does not block behind macOS Keychain access. Packaged builds
	// always use the OS-protected store.
	if (!app.isPackaged && process.env.AO_CLOUD_AUTH_MEMORY_ONLY === "1") return false;
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

async function readAuthStore(dataDir: string): Promise<AuthStore> {
	const memoryStore = memoryStores.get(dataDir);
	if (memoryStore) return memoryStore;
	let encryptedStore: Buffer;
	try {
		encryptedStore = await readFile(storePath(dataDir));
	} catch {
		// Avoid touching the OS credential store on a fresh install. On macOS,
		// merely probing safeStorage can block on a Keychain prompt even though
		// there is no AO Cloud credential to decrypt yet.
		return emptyStore();
	}
	if (!protectedStorageAvailable()) {
		await rm(storePath(dataDir), { force: true });
		return emptyStore();
	}
	try {
		return decodeStore(encryptedStore);
	} catch {
		await rm(storePath(dataDir), { force: true });
		return emptyStore();
	}
}

async function writeAuthStore(dataDir: string, store: AuthStore): Promise<void> {
	if (!protectedStorageAvailable()) {
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

async function removeAuthStore(dataDir: string): Promise<void> {
	memoryStores.delete(dataDir);
	await Promise.all([
		rm(storePath(dataDir), { force: true }),
		rm(path.join(dataDir, LEGACY_SESSION_FILE), { force: true }),
	]);
}

function toStoredSession(response: AuthResponse): StoredSession {
	return {
		authProvider: "google",
		user: {
			id: response.user.id,
			email: response.user.email,
			displayName: response.user.displayName,
		},
		organizations: response.organizations,
		storedAt: new Date().toISOString(),
		accessToken: response.accessToken,
		refreshToken: response.refreshToken,
		expiresAt: response.expiresAt,
	};
}

function publicAccount(session: StoredSession): CloudAccount {
	return {
		authProvider: session.authProvider,
		user: session.user,
		organizations: session.organizations,
		storedAt: session.storedAt,
	};
}

function tokenExpiresSoon(session: StoredSession): boolean {
	const expiry = Date.parse(session.expiresAt);
	return !Number.isFinite(expiry) || Date.now() >= expiry - 60_000;
}

async function cloudRequest<T>(route: string, init: RequestInit): Promise<T> {
	const response = await fetch(`${CLOUD_API_URL}${route}`, {
		...init,
		headers: { "Content-Type": "application/json", ...init.headers },
	});
	const body = (await response.json().catch(() => null)) as
		| (T & { message?: string; code?: string })
		| null;
	if (!response.ok || !body) {
		throw Object.assign(new Error(body?.message || `AO Cloud request failed (${response.status})`), {
			status: response.status,
			code: body?.code,
		});
	}
	return body;
}

function isTerminalRefreshFailure(error: unknown): boolean {
	if (!error || typeof error !== "object") return false;
	const candidate = error as { status?: unknown; code?: unknown };
	return candidate.status === 401 || candidate.code === "INVALID_REFRESH_TOKEN";
}

export async function getCloudSession(dataDir: string): Promise<CloudAccount | null> {
	if (!cloudAuthConfigured()) return null;
	const activeRefresh = refreshes.get(dataDir);
	if (activeRefresh) return activeRefresh;
	const store = await readAuthStore(dataDir);
	if (!store.session) return null;
	if (!tokenExpiresSoon(store.session)) return publicAccount(store.session);
	const pendingRefresh = refreshes.get(dataDir);
	if (pendingRefresh) return pendingRefresh;
	const refresh = refreshCloudSession(dataDir, store.session, authGeneration(dataDir));
	refreshes.set(dataDir, refresh);
	try {
		return await refresh;
	} finally {
		if (refreshes.get(dataDir) === refresh) refreshes.delete(dataDir);
	}
}

async function refreshCloudSession(
	dataDir: string,
	storedSession: StoredSession,
	generation: number,
): Promise<CloudAccount | null> {
	try {
		const response = await cloudRequest<AuthResponse>("/api/cloud/v1/auth/refresh", {
			method: "POST",
			body: JSON.stringify({ refreshToken: storedSession.refreshToken }),
		});
		const session = toStoredSession(response);
		return withAuthMutation(dataDir, async () => {
			if (authGeneration(dataDir) !== generation) return null;
			const currentStore = await readAuthStore(dataDir);
			if (currentStore.session?.refreshToken !== storedSession.refreshToken) return null;
			await writeAuthStore(dataDir, { ...currentStore, session });
			return publicAccount(session);
		});
	} catch (error) {
		if (!isTerminalRefreshFailure(error)) return publicAccount(storedSession);
		await withAuthMutation(dataDir, async () => {
			if (authGeneration(dataDir) !== generation) return;
			const currentStore = await readAuthStore(dataDir);
			if (currentStore.session?.refreshToken === storedSession.refreshToken) await removeAuthStore(dataDir);
		});
		return null;
	}
}

export async function getCloudAccessToken(dataDir: string): Promise<string> {
	const account = await getCloudSession(dataDir);
	if (!account) throw new Error("Sign in to AO Cloud first.");
	const session = (await readAuthStore(dataDir)).session;
	if (!session) throw new Error("Sign in to AO Cloud first.");
	return session.accessToken;
}

function base64url(value: Buffer): string {
	return value.toString("base64url");
}

async function waitForGoogleCode(
	state: string,
	timeoutMs = PKCE_TTL_MS,
): Promise<{ redirectURI: string; code: Promise<{ code: string; redirectURI: string }> }> {
	let settle: ((value: { code: string; redirectURI: string }) => void) | undefined;
	let reject: ((error: Error) => void) | undefined;
	const result = new Promise<{ code: string; redirectURI: string }>((resolve, rejectResult) => {
		settle = resolve;
		reject = rejectResult;
	});
	const server = createServer((request, response) => {
		const requestURL = new URL(request.url || "/", "http://127.0.0.1");
		if (requestURL.pathname !== "/callback") {
			response.writeHead(404).end();
			return;
		}
		const error = requestURL.searchParams.get("error");
		const callbackState = requestURL.searchParams.get("state");
		const code = requestURL.searchParams.get("code");
		response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
		response.end("<!doctype html><title>AO Cloud</title><p>You can return to Agent Orchestrator.</p>");
		if (error) reject?.(new Error(`Google sign-in failed: ${error}`));
		else if (callbackState !== state) reject?.(new Error("Google callback state did not match."));
		else if (!code) reject?.(new Error("Google callback is incomplete."));
		else {
			const address = server.address() as AddressInfo;
			settle?.({ code, redirectURI: `http://127.0.0.1:${address.port}/callback` });
		}
	});
	await new Promise<void>((resolve, rejectListen) => {
		server.once("error", rejectListen);
		server.listen(0, "127.0.0.1", resolve);
	});
	const address = server.address() as AddressInfo;
	const redirectURI = `http://127.0.0.1:${address.port}/callback`;
	const timer = setTimeout(() => reject?.(new Error("The Google sign-in request expired.")), timeoutMs);
	return {
		redirectURI,
		code: result.finally(() => {
			clearTimeout(timer);
			server.close();
		}),
	};
}

export async function beginCloudSignIn(dataDir: string): Promise<CloudAccount> {
	if (!cloudAuthConfigured()) throw new Error("AO Cloud Google sign-in is not configured.");
	const state = base64url(randomBytes(24));
	const codeVerifier = base64url(randomBytes(48));
	const codeChallenge = base64url(createHash("sha256").update(codeVerifier).digest());
	invalidateAuthOperations(dataDir);
	const store = await readAuthStore(dataDir);
	await writeAuthStore(dataDir, {
		...store,
		pkce: { codeVerifier, state, expiresAt: Date.now() + PKCE_TTL_MS },
	});

	const pendingCode = await waitForGoogleCode(state);
	const redirectURI = pendingCode.redirectURI;
	const authorize = new URL("https://accounts.google.com/o/oauth2/v2/auth");
	authorize.search = new URLSearchParams({
		client_id: GOOGLE_CLIENT_ID,
		redirect_uri: redirectURI,
		response_type: "code",
		scope: "openid email profile",
		code_challenge: codeChallenge,
		code_challenge_method: "S256",
		state,
		prompt: "select_account",
	}).toString();
	await shell.openExternal(authorize.toString());
	const callback = await pendingCode.code;
	const tokenBody = new URLSearchParams({
		client_id: GOOGLE_CLIENT_ID,
		code: callback.code,
		code_verifier: codeVerifier,
		redirect_uri: callback.redirectURI,
		grant_type: "authorization_code",
	});
	if (GOOGLE_CLIENT_SECRET) tokenBody.set("client_secret", GOOGLE_CLIENT_SECRET);
	const tokenResponse = await fetch("https://oauth2.googleapis.com/token", {
		method: "POST",
		headers: { "Content-Type": "application/x-www-form-urlencoded" },
		body: tokenBody,
	});
	const token = (await tokenResponse.json().catch(() => null)) as { id_token?: string } | null;
	if (!tokenResponse.ok || !token?.id_token) throw new Error("Google did not return a valid identity token.");
	const response = await cloudRequest<AuthResponse>("/api/cloud/v1/auth/google", {
		method: "POST",
		body: JSON.stringify({ idToken: token.id_token }),
	});
	const session = toStoredSession(response);
	await writeAuthStore(dataDir, { session, pkce: null });
	return publicAccount(session);
}

export async function handleCloudDeepLink(_rawURL: string, _dataDir: string): Promise<CloudAccount | null> {
	return null;
}

export async function signOutCloud(dataDir: string): Promise<void> {
	invalidateAuthOperations(dataDir);
	const session = (await readAuthStore(dataDir)).session;
	if (session) {
		await cloudRequest("/api/cloud/v1/auth/logout", {
			method: "POST",
			body: JSON.stringify({ refreshToken: session.refreshToken }),
		}).catch(() => undefined);
	}
	await withAuthMutation(dataDir, () => removeAuthStore(dataDir));
}

function signInFailureDetail(error: unknown): string {
	const message = error instanceof Error ? error.message.toLowerCase() : "";
	if (message.includes("cancel") || message.includes("access_denied")) return "Google sign-in was cancelled.";
	if (message.includes("expired")) return "The Google sign-in request expired. Start sign-in again.";
	if (message.includes("state")) return "The Google response could not be verified. Start sign-in again.";
	if (message.includes("network") || message.includes("fetch")) return "AO could not reach Google or AO Cloud. Check your connection.";
	return "Agent Orchestrator could not complete Google sign-in. Please try again.";
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
		app.setAsDefaultProtocolClient("ao-app", process.execPath, [path.resolve(process.argv[1])]);
		return;
	}
	app.setAsDefaultProtocolClient("ao-app");
}

export function installCloudIPC(
	getDataDir: () => string,
	notifyRenderers: (session: CloudAccount | null) => void,
): void {
	ipcMain.handle("cloud:getSession", () => getCloudSession(getDataDir()));
	ipcMain.handle("cloud:signIn", async () => {
		if (!cloudAuthConfigured()) {
			await dialog.showMessageBox({
				type: "info",
				title: "AO Cloud not configured",
				message: "AO Cloud Google sign-in is not configured.",
				detail: "Set VITE_AO_CLOUD_API_URL and VITE_AO_CLOUD_GOOGLE_CLIENT_ID, then restart AO.",
			});
			return;
		}
		try {
			const account = await beginCloudSignIn(getDataDir());
			notifyRenderers(account);
		} catch (error) {
			await showCloudSignInFailure(error);
		}
	});
	ipcMain.handle("cloud:signOut", async () => {
		await signOutCloud(getDataDir());
		notifyRenderers(null);
	});
}
