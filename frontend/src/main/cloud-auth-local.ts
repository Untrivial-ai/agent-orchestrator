// Dev-only email/password sign-in against a LOCAL Docker control plane running
// AO_CLOUD_LOCAL_AUTH. This lets an unpackaged desktop build exercise the whole
// AO cloud flow with zero NodeOps/WorkOS: the CP mints an OPAQUE token (prefix
// "ao_local_", not a JWT) which is stored in the same safeStorage-encrypted
// store as a WorkOS session, so cloud-cp-proxy.ts and the terminal WS authorize
// with no further changes.
//
// Hard gate: local auth is permitted ONLY when the build is unpackaged (or the
// explicit AO_CLOUD_LOCAL_AUTH_DEV=1 override is set) AND the control-plane URL
// resolves to loopback (127.0.0.1/localhost). An email/password must never be
// sent to a remote host. The gate is enforced here in the main process on every
// register/login attempt; the renderer only mirrors it for UI visibility.

import { app, ipcMain } from "electron";
import type {
	CloudAccount,
	CloudOrganization,
} from "../shared/cloud-account";
import {
	invalidateAuthOperations,
	isLocalSession,
	publicAccount,
	type StoredSession,
	withAuthMutation,
	writeAuthStore,
} from "./cloud-auth";

const LOCAL_AUTH_PREFIX = "/api/cloud/v1/auth/local";
/** Opaque tokens the local CP mints all carry this prefix (never a JWT). */
export const LOCAL_TOKEN_PREFIX = "ao_local_";

// ---------------------------------------------------------------------------
// The gate — one predicate, in one place, so it stays unit-testable.
// ---------------------------------------------------------------------------

export interface LocalAuthGateOptions {
	/** Electron's app.isPackaged for this build. */
	isPackaged: boolean;
	/** The AO_CLOUD_LOCAL_AUTH_DEV=1 escape hatch for packaged dev testing. */
	devEnvEnabled: boolean;
	/** The resolved cloud control-plane base URL (settings.cloudControlPlaneUrl). */
	cpUrl: string;
}

/**
 * True only for an http/https URL whose host is loopback. This is the security
 * boundary: it guarantees an email/password never leaves the machine.
 */
export function isLoopbackCpUrl(cpUrl: string): boolean {
	let url: URL;
	try {
		url = new URL(cpUrl);
	} catch {
		return false;
	}
	if (url.protocol !== "http:" && url.protocol !== "https:") return false;
	return url.hostname === "127.0.0.1" || url.hostname === "localhost";
}

/** Pure gate predicate: dev build (or explicit override) AND loopback CP. */
export function localAuthAllowed(options: LocalAuthGateOptions): boolean {
	const devAllowed = !options.isPackaged || options.devEnvEnabled;
	return devAllowed && isLoopbackCpUrl(options.cpUrl);
}

/** Gate against the live Electron/app + process env for a given CP URL. */
export function localAuthAvailable(cpUrl: string): boolean {
	return localAuthAllowed({
		isPackaged: app.isPackaged,
		devEnvEnabled: process.env.AO_CLOUD_LOCAL_AUTH_DEV === "1",
		cpUrl,
	});
}

// ---------------------------------------------------------------------------
// CP transport
// ---------------------------------------------------------------------------

export class LocalAuthError extends Error {
	constructor(
		readonly status: number,
		message: string,
	) {
		super(message);
		this.name = "LocalAuthError";
	}
}

export interface LocalRegisterInput {
	cpUrl: string;
	email: string;
	displayName: string;
	password: string;
	orgSlug: string;
	orgName: string;
}

export interface LocalLoginInput {
	cpUrl: string;
	email: string;
	password: string;
}

export interface LocalAuthOptions {
	/** Transport override for tests; defaults to the main-process global fetch. */
	fetchImpl?: typeof fetch;
}

interface AuthResponse {
	token: string;
	expiresAt?: string;
	user: {
		id: string;
		email: string;
		displayName: string;
		authProvider?: string;
	};
	organizations?: CloudOrganization[];
}

function normalizeCpUrl(cpUrl: string): string {
	return new URL(cpUrl).origin;
}

function errorMessage(parsed: unknown, status: number): string {
	if (parsed && typeof parsed === "object") {
		const body = parsed as { message?: unknown; error?: unknown };
		if (typeof body.message === "string" && body.message !== "") return body.message;
		if (typeof body.error === "string" && body.error !== "") return body.error;
	}
	return `The control plane rejected the request (status ${status}).`;
}

function isAuthResponse(value: unknown): value is AuthResponse {
	if (!value || typeof value !== "object") return false;
	const body = value as Record<string, unknown>;
	if (typeof body.token !== "string" || body.token === "") return false;
	const user = body.user;
	if (!user || typeof user !== "object") return false;
	const u = user as Record<string, unknown>;
	return typeof u.id === "string" && typeof u.email === "string";
}

async function postLocalAuth(
	cpUrl: string,
	route: "register" | "login",
	body: unknown,
	fetchImpl: typeof fetch,
): Promise<AuthResponse> {
	// Defence in depth: never dial a non-loopback host from this module even if
	// a caller skipped the gate. Prevents credentials leaking to a remote CP.
	if (!isLoopbackCpUrl(cpUrl)) {
		throw new LocalAuthError(0, "Local sign-in is only allowed against a loopback control plane.");
	}
	const url = `${normalizeCpUrl(cpUrl)}${LOCAL_AUTH_PREFIX}/${route}`;
	let response: Response;
	try {
		response = await fetchImpl(url, {
			method: "POST",
			headers: { "Content-Type": "application/json", Accept: "application/json" },
			body: JSON.stringify(body),
		});
	} catch (error) {
		throw new LocalAuthError(0, error instanceof Error ? error.message : String(error));
	}
	const text = await response.text();
	let parsed: unknown = null;
	if (text !== "") {
		try {
			parsed = JSON.parse(text);
		} catch {
			parsed = null;
		}
	}
	if (!response.ok) {
		throw new LocalAuthError(response.status, errorMessage(parsed, response.status));
	}
	if (!isAuthResponse(parsed)) {
		throw new LocalAuthError(response.status, "The control plane returned an unexpected response.");
	}
	return parsed;
}

function toStoredSession(cpUrl: string, response: AuthResponse): StoredSession {
	return {
		authProvider: "local",
		accessToken: response.token,
		cpBaseUrl: normalizeCpUrl(cpUrl),
		user: {
			id: response.user.id,
			email: response.user.email,
			displayName: response.user.displayName || response.user.email,
		},
		organizations: response.organizations ?? [],
		storedAt: new Date().toISOString(),
	};
}

async function persistLocalSession(
	dataDir: string,
	session: StoredSession,
): Promise<CloudAccount> {
	// Any in-flight WorkOS refresh for this data dir is now stale.
	invalidateAuthOperations(dataDir);
	return withAuthMutation(dataDir, async () => {
		await writeAuthStore(dataDir, { session, pkce: null });
		return publicAccount(session);
	});
}

// ---------------------------------------------------------------------------
// Public operations
// ---------------------------------------------------------------------------

export async function registerLocal(
	dataDir: string,
	input: LocalRegisterInput,
	options: LocalAuthOptions = {},
): Promise<CloudAccount> {
	const fetchImpl = options.fetchImpl ?? fetch;
	const response = await postLocalAuth(
		input.cpUrl,
		"register",
		{
			email: input.email,
			displayName: input.displayName,
			password: input.password,
			orgSlug: input.orgSlug,
			orgName: input.orgName,
		},
		fetchImpl,
	);
	return persistLocalSession(dataDir, toStoredSession(input.cpUrl, response));
}

export async function loginLocal(
	dataDir: string,
	input: LocalLoginInput,
	options: LocalAuthOptions = {},
): Promise<CloudAccount> {
	const fetchImpl = options.fetchImpl ?? fetch;
	const response = await postLocalAuth(
		input.cpUrl,
		"login",
		{ email: input.email, password: input.password },
		fetchImpl,
	);
	return persistLocalSession(dataDir, toStoredSession(input.cpUrl, response));
}

/**
 * Best-effort server-side revoke of a stored local session's opaque token.
 * Never removes the store (sign-out does that uniformly) and never throws:
 * the token has a bounded TTL server-side regardless.
 */
export async function revokeLocalSession(
	session: StoredSession,
	options: LocalAuthOptions = {},
): Promise<void> {
	if (!isLocalSession(session) || !session.cpBaseUrl || !session.accessToken) return;
	if (!isLoopbackCpUrl(session.cpBaseUrl)) return;
	const fetchImpl = options.fetchImpl ?? fetch;
	try {
		await fetchImpl(`${normalizeCpUrl(session.cpBaseUrl)}${LOCAL_AUTH_PREFIX}/logout`, {
			method: "POST",
			headers: { Authorization: `Bearer ${session.accessToken}` },
		});
	} catch {
		// Ignore: revocation is best-effort.
	}
}

// ---------------------------------------------------------------------------
// IPC — dev-only local sign-in surface. Registered from main.ts alongside
// installCloudIPC. The register/login handlers re-enforce the gate server-side
// so a compromised renderer can never send credentials to a remote host.
// ---------------------------------------------------------------------------

export function installCloudLocalAuthIPC(
	getDataDir: () => string,
	notifyRenderers: (session: CloudAccount | null) => void,
): void {
	ipcMain.handle("cloud:localAuthAvailable", (_event, cpUrl: unknown) =>
		typeof cpUrl === "string" && localAuthAvailable(cpUrl),
	);

	ipcMain.handle("cloud:localRegister", async (_event, input: LocalRegisterInput) => {
		if (!localAuthAvailable(input.cpUrl)) {
			throw new Error("Local sign-in is not available for this build or control plane.");
		}
		const account = await registerLocal(getDataDir(), input);
		notifyRenderers(account);
		return account;
	});

	ipcMain.handle("cloud:localLogin", async (_event, input: LocalLoginInput) => {
		if (!localAuthAvailable(input.cpUrl)) {
			throw new Error("Local sign-in is not available for this build or control plane.");
		}
		const account = await loginLocal(getDataDir(), input);
		notifyRenderers(account);
		return account;
	});
}
