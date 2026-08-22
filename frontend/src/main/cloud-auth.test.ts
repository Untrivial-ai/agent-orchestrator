import { get } from "node:http";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	decryptString: vi.fn((value: Buffer) => value.toString("utf8")),
	encryptString: vi.fn((value: string) => Buffer.from(value, "utf8")),
	isEncryptionAvailable: vi.fn(() => true),
	encryptionAvailable: true,
	selectedStorageBackend: "gnome_libsecret",
	openExternal: vi.fn(),
	showMessageBox: vi.fn(),
}));

vi.mock("electron", () => ({
	app: { setAsDefaultProtocolClient: vi.fn() },
	dialog: { showMessageBox: mocks.showMessageBox },
	ipcMain: { handle: vi.fn() },
	safeStorage: {
		decryptString: mocks.decryptString,
		encryptString: mocks.encryptString,
		getSelectedStorageBackend: () => mocks.selectedStorageBackend,
		isEncryptionAvailable: mocks.isEncryptionAvailable,
	},
	shell: { openExternal: mocks.openExternal },
}));

import {
	beginCloudSignIn,
	getCloudAccessToken,
	getCloudSession,
	showCloudSignInFailure,
	signOutCloud,
} from "./cloud-auth";

const sessionResponse = (accessToken = "access_123", refreshToken = "ao_refresh_123", expiresAt?: string) => ({
	accessToken,
	refreshToken,
	expiresAt: expiresAt ?? new Date(Date.now() + 15 * 60_000).toISOString(),
	user: { id: "user_123", email: "person@example.com", displayName: "Person Example", authProvider: "google" },
	organizations: [{ id: "org_123", slug: "personal-user", displayName: "Person's organization", role: "owner" }],
});

describe("native Google authentication", () => {
	let dataDir: string;
	let fetchMock: ReturnType<typeof vi.fn>;

	beforeEach(async () => {
		vi.clearAllMocks();
		mocks.encryptionAvailable = true;
		mocks.isEncryptionAvailable.mockImplementation(() => mocks.encryptionAvailable);
		mocks.selectedStorageBackend = "gnome_libsecret";
		dataDir = await mkdtemp(path.join(os.tmpdir(), "ao-cloud-auth-"));
		fetchMock = vi.fn(async (input: string | URL | Request) => {
			const url = String(input instanceof Request ? input.url : input);
			if (url === "https://oauth2.googleapis.com/token") {
				return new Response(JSON.stringify({ id_token: "google_id_token" }), { status: 200 });
			}
			if (url.endsWith("/api/cloud/v1/auth/google")) {
				return new Response(JSON.stringify(sessionResponse()), { status: 200 });
			}
			if (url.endsWith("/api/cloud/v1/auth/refresh")) {
				return new Response(JSON.stringify(sessionResponse("access_456", "ao_refresh_456")), { status: 200 });
			}
			if (url.endsWith("/api/cloud/v1/auth/logout")) return new Response(null, { status: 204 });
			throw new Error(`Unexpected request: ${url}`);
		});
		vi.stubGlobal("fetch", fetchMock);
		mocks.openExternal.mockImplementation(async (raw: string) => {
			const authorize = new URL(raw);
			const redirect = new URL(authorize.searchParams.get("redirect_uri")!);
			redirect.searchParams.set("code", "google_code");
			redirect.searchParams.set("state", authorize.searchParams.get("state")!);
			await new Promise<void>((resolve, reject) => get(redirect, (response) => {
				response.resume();
				response.on("end", resolve);
			}).on("error", reject));
		});
	});

	afterEach(async () => {
		vi.unstubAllGlobals();
		await rm(dataDir, { recursive: true, force: true });
	});

	it("uses Google loopback PKCE and exchanges the ID token with AO Cloud", async () => {
		const account = await beginCloudSignIn(dataDir);
		expect(account).toMatchObject({
			authProvider: "google",
			user: { email: "person@example.com" },
			organizations: [{ id: "org_123", role: "owner" }],
		});
		expect(account).not.toHaveProperty("accessToken");
		const authorize = new URL(mocks.openExternal.mock.calls[0][0]);
		expect(authorize.hostname).toBe("accounts.google.com");
		expect(authorize.searchParams.get("code_challenge_method")).toBe("S256");
		expect(authorize.searchParams.get("redirect_uri")).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/callback$/);
		await expect(getCloudAccessToken(dataDir)).resolves.toBe("access_123");
	});

	it("does not probe protected storage before a cloud session exists", async () => {
		await expect(getCloudSession(dataDir)).resolves.toBeNull();
		expect(mocks.isEncryptionAvailable).not.toHaveBeenCalled();
	});

	it("keeps credentials process-local when protected storage is unavailable", async () => {
		mocks.encryptionAvailable = false;
		await beginCloudSignIn(dataDir);
		await expect(readFile(path.join(dataDir, "cloud-auth.bin"))).rejects.toMatchObject({ code: "ENOENT" });
		await expect(getCloudSession(dataDir)).resolves.toMatchObject({ user: { email: "person@example.com" } });
	});

	it("rotates an expired AO refresh token once for concurrent callers", async () => {
		fetchMock.mockImplementation(async (input: string | URL | Request) => {
			const url = String(input instanceof Request ? input.url : input);
			if (url === "https://oauth2.googleapis.com/token") return new Response(JSON.stringify({ id_token: "google_id_token" }));
			if (url.endsWith("/auth/google")) return new Response(JSON.stringify(sessionResponse("old", "ao_refresh_old", new Date(1).toISOString())));
			if (url.endsWith("/auth/refresh")) return new Response(JSON.stringify(sessionResponse("new", "ao_refresh_new")));
			return new Response(null, { status: 204 });
		});
		await beginCloudSignIn(dataDir);
		const [first, second] = await Promise.all([getCloudSession(dataDir), getCloudSession(dataDir)]);
		expect(first).toEqual(second);
		expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/auth/refresh"))).toHaveLength(1);
		await expect(getCloudAccessToken(dataDir)).resolves.toBe("new");
	});

	it("clears local credentials on sign-out", async () => {
		await beginCloudSignIn(dataDir);
		await signOutCloud(dataDir);
		await expect(getCloudSession(dataDir)).resolves.toBeNull();
		await expect(readFile(path.join(dataDir, "cloud-auth.bin"))).rejects.toMatchObject({ code: "ENOENT" });
	});

	it("shows a bounded sign-in failure without leaking the raw error", async () => {
		await showCloudSignInFailure(new Error("The Google sign-in request expired with secret details"));
		expect(mocks.showMessageBox).toHaveBeenCalledWith({
			type: "error",
			title: "AO Cloud sign-in failed",
			message: "Unable to sign in to AO Cloud",
			detail: "The Google sign-in request expired. Start sign-in again.",
		});
	});
});
