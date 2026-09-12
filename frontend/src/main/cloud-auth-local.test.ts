import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// cloud-auth-local imports store helpers from cloud-auth, which constructs a
// WorkOS client and reads Electron globals at module load — mock both so the
// suite runs in Node with no Electron/WorkOS.
const mocks = vi.hoisted(() => ({
	isPackaged: false,
	encryptionAvailable: true,
	selectedStorageBackend: "gnome_libsecret",
}));

vi.mock("@workos-inc/node", () => ({
	createWorkOS: () => ({
		userManagement: {
			authenticateWithCode: vi.fn(),
			authenticateWithRefreshToken: vi.fn(),
			getAuthorizationUrlWithPKCE: vi.fn(),
		},
	}),
}));

vi.mock("electron", () => ({
	app: {
		get isPackaged() {
			return mocks.isPackaged;
		},
		setAsDefaultProtocolClient: vi.fn(),
	},
	dialog: { showMessageBox: vi.fn() },
	ipcMain: { handle: vi.fn() },
	safeStorage: {
		decryptString: (value: Buffer) => value.toString("utf8"),
		encryptString: (value: string) => Buffer.from(value, "utf8"),
		getSelectedStorageBackend: () => mocks.selectedStorageBackend,
		isEncryptionAvailable: () => mocks.encryptionAvailable,
	},
	shell: { openExternal: vi.fn() },
}));

import { getCloudAccessToken, getCloudSession, readAuthStore, signOutCloud } from "./cloud-auth";
import {
	isLoopbackCpUrl,
	loginLocal,
	localAuthAllowed,
	registerLocal,
	revokeLocalSession,
} from "./cloud-auth-local";

const CP_URL = "http://127.0.0.1:8081";

function jsonResponse(status: number, body: unknown): Response {
	return {
		ok: status >= 200 && status < 300,
		status,
		text: async () => JSON.stringify(body),
	} as unknown as Response;
}

const REGISTER_BODY = {
	token: "ao_local_register_token",
	expiresAt: "2099-01-01T00:00:00Z",
	user: { id: "user_1", email: "dev@example.com", displayName: "Dev One", authProvider: "local" },
	organizations: [{ id: "org_1", slug: "acme", displayName: "Acme", role: "owner" }],
};

const LOGIN_BODY = {
	token: "ao_local_login_token",
	expiresAt: "2099-01-01T00:00:00Z",
	user: { id: "user_1", email: "dev@example.com", displayName: "Dev One", authProvider: "local" },
	organizations: [{ id: "org_1", slug: "acme", displayName: "Acme", role: "owner" }],
};

describe("local-auth dev+loopback gate", () => {
	it("allows an unpackaged dev build against a loopback control plane", () => {
		expect(localAuthAllowed({ isPackaged: false, devEnvEnabled: false, cpUrl: "http://127.0.0.1:8081" })).toBe(true);
		expect(localAuthAllowed({ isPackaged: false, devEnvEnabled: false, cpUrl: "http://localhost:8081" })).toBe(true);
	});

	it("rejects a packaged build unless the explicit dev override is set", () => {
		expect(localAuthAllowed({ isPackaged: true, devEnvEnabled: false, cpUrl: "http://127.0.0.1:8081" })).toBe(false);
		expect(localAuthAllowed({ isPackaged: true, devEnvEnabled: true, cpUrl: "http://127.0.0.1:8081" })).toBe(true);
	});

	it("rejects a non-loopback control plane even in a dev build", () => {
		expect(localAuthAllowed({ isPackaged: false, devEnvEnabled: true, cpUrl: "https://api.aoagents.dev" })).toBe(false);
		expect(localAuthAllowed({ isPackaged: false, devEnvEnabled: true, cpUrl: "http://192.168.0.5:8081" })).toBe(false);
	});

	it("classifies loopback URLs precisely", () => {
		expect(isLoopbackCpUrl("http://127.0.0.1:8081")).toBe(true);
		expect(isLoopbackCpUrl("http://localhost")).toBe(true);
		expect(isLoopbackCpUrl("https://localhost:8081")).toBe(true);
		expect(isLoopbackCpUrl("https://api.aoagents.dev")).toBe(false);
		expect(isLoopbackCpUrl("ftp://127.0.0.1")).toBe(false);
		expect(isLoopbackCpUrl("not a url")).toBe(false);
	});
});

describe("local-auth register/login/logout", () => {
	let dataDir: string;

	beforeEach(async () => {
		vi.clearAllMocks();
		mocks.encryptionAvailable = true;
		mocks.selectedStorageBackend = "gnome_libsecret";
		dataDir = await mkdtemp(path.join(os.tmpdir(), "ao-cloud-local-"));
	});

	afterEach(async () => {
		await rm(dataDir, { recursive: true, force: true });
	});

	it("registers, stores the opaque token + identity, and round-trips the session", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(jsonResponse(201, REGISTER_BODY));
		const account = await registerLocal(
			dataDir,
			{
				cpUrl: CP_URL,
				email: "dev@example.com",
				displayName: "Dev One",
				password: "correct-horse-battery",
				orgSlug: "acme",
				orgName: "Acme",
			},
			{ fetchImpl },
		);

		expect(fetchImpl).toHaveBeenCalledWith(
			"http://127.0.0.1:8081/api/cloud/v1/auth/local/register",
			expect.objectContaining({ method: "POST" }),
		);
		expect(account).toMatchObject({
			authProvider: "local",
			user: { id: "user_1", email: "dev@example.com", displayName: "Dev One" },
			organizations: [{ id: "org_1", slug: "acme", role: "owner" }],
		});
		// The opaque token must never appear on the public account.
		expect(account).not.toHaveProperty("accessToken");

		// Store round-trip: session is readable and the token is the opaque one.
		await expect(getCloudSession(dataDir)).resolves.toMatchObject({
			authProvider: "local",
			user: { email: "dev@example.com" },
		});
		await expect(getCloudAccessToken(dataDir)).resolves.toBe("ao_local_register_token");
	});

	it("logs in and returns the local session token to the CP proxy accessor", async () => {
		const fetchImpl = vi.fn().mockResolvedValue(jsonResponse(200, LOGIN_BODY));
		const account = await loginLocal(
			dataDir,
			{ cpUrl: CP_URL, email: "dev@example.com", password: "correct-horse-battery" },
			{ fetchImpl },
		);

		expect(fetchImpl).toHaveBeenCalledWith(
			"http://127.0.0.1:8081/api/cloud/v1/auth/local/login",
			expect.objectContaining({ method: "POST" }),
		);
		expect(account.authProvider).toBe("local");
		await expect(getCloudAccessToken(dataDir)).resolves.toBe("ao_local_login_token");

		// A stored token is encrypted on disk (safeStorage passthrough here).
		await expect(readFile(path.join(dataDir, "cloud-auth.bin"))).resolves.toBeInstanceOf(Buffer);
	});

	it("surfaces the control plane error message on a failed login", async () => {
		const fetchImpl = vi
			.fn()
			.mockResolvedValue(jsonResponse(401, { error: "invalid_credentials", message: "Invalid email or password." }));
		await expect(
			loginLocal(dataDir, { cpUrl: CP_URL, email: "dev@example.com", password: "nope" }, { fetchImpl }),
		).rejects.toThrow("Invalid email or password.");
		// Nothing is stored on failure.
		await expect(getCloudSession(dataDir)).resolves.toBeNull();
	});

	it("refuses to dial a non-loopback control plane even if the gate was skipped", async () => {
		const fetchImpl = vi.fn();
		await expect(
			loginLocal(
				dataDir,
				{ cpUrl: "https://api.aoagents.dev", email: "dev@example.com", password: "correct-horse-battery" },
				{ fetchImpl },
			),
		).rejects.toThrow("loopback");
		expect(fetchImpl).not.toHaveBeenCalled();
	});

	it("revokes the opaque token server-side on sign-out, then clears the store", async () => {
		const loginFetch = vi.fn().mockResolvedValue(jsonResponse(200, LOGIN_BODY));
		await loginLocal(
			dataDir,
			{ cpUrl: CP_URL, email: "dev@example.com", password: "correct-horse-battery" },
			{ fetchImpl: loginFetch },
		);

		const stored = await readAuthStore(dataDir);
		const revokeFetch = vi.fn().mockResolvedValue(jsonResponse(200, {}));
		await revokeLocalSession(stored.session!, { fetchImpl: revokeFetch });
		expect(revokeFetch).toHaveBeenCalledWith(
			"http://127.0.0.1:8081/api/cloud/v1/auth/local/logout",
			expect.objectContaining({
				method: "POST",
				headers: { Authorization: "Bearer ao_local_login_token" },
			}),
		);

		await signOutCloud(dataDir);
		await expect(getCloudSession(dataDir)).resolves.toBeNull();
	});

	it("never throws from a best-effort revoke when the CP is unreachable", async () => {
		const loginFetch = vi.fn().mockResolvedValue(jsonResponse(200, LOGIN_BODY));
		await loginLocal(
			dataDir,
			{ cpUrl: CP_URL, email: "dev@example.com", password: "correct-horse-battery" },
			{ fetchImpl: loginFetch },
		);
		const stored = await readAuthStore(dataDir);
		const revokeFetch = vi.fn().mockRejectedValue(new Error("connection refused"));
		await expect(revokeLocalSession(stored.session!, { fetchImpl: revokeFetch })).resolves.toBeUndefined();
	});
});
