import { createCipheriv, createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, stat, symlink, truncate, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import Database from "better-sqlite3";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BrowserHistoryStore } from "./browser-history-store";
import { BrowserProfileImportService, decryptWindowsChromiumCookie } from "./browser-profile-import";
import { BrowserProfileStore } from "./browser-profile-store";
import { BROWSER_PROFILE_MAX_COUNT } from "../shared/browser-profiles";

const temporaryDirectories: string[] = [];

afterEach(async () => {
	await Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

async function fixtureRoot(): Promise<string> {
	const directory = await mkdtemp(path.join(os.tmpdir(), "ao-browser-import-"));
	temporaryDirectories.push(directory);
	return directory;
}

async function createChromeFixture(root: string): Promise<{ localAppData: string; profileRoot: string }> {
	const localAppData = path.join(root, "local");
	const userData = path.join(localAppData, "Google", "Chrome", "User Data");
	const profileRoot = path.join(userData, "Default");
	await mkdir(path.join(profileRoot, "Network"), { recursive: true });
	await writeFile(path.join(userData, "Local State"), JSON.stringify({ profile: { info_cache: { Default: { name: "Personal" } } } }));

	const cookies = new Database(path.join(profileRoot, "Network", "Cookies"));
	cookies.exec(`
		CREATE TABLE cookies (
			host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT,
			expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER,
			top_frame_site_key TEXT, last_access_utc INTEGER, creation_utc INTEGER
		)
	`);
	const future = chromiumMicros("2030-01-01T00:00:00.000Z");
	const past = chromiumMicros("2020-01-01T00:00:00.000Z");
	const insertCookie = cookies.prepare("INSERT INTO cookies VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)");
	insertCookie.run(".github.com", "session", "usable", Buffer.alloc(0), "/", future, 1, 1, 1, "", future, future);
	insertCookie.run(".github.com", "empty", "", Buffer.alloc(0), "/", future, 1, 0, 1, "", future - 1, future - 1);
	insertCookie.run(".github.com", "app-bound", "", Buffer.from("v20-unavailable"), "/", future, 1, 1, 1, "", future - 2, future - 2);
	insertCookie.run(".github.com", "expired", "old", Buffer.alloc(0), "/", past, 1, 0, 1, "", past, past);
	insertCookie.run(".example.com", "filtered", "nope", Buffer.alloc(0), "/", future, 1, 0, 1, "", future - 3, future - 3);
	insertCookie.run(".isolated.example", "partitioned", "private", Buffer.alloc(0), "/", future, 1, 1, 1, "https://top.example", future + 1, future + 1);
	cookies.close();

	const history = new Database(path.join(profileRoot, "History"));
	history.exec("CREATE TABLE urls (url TEXT, title TEXT, visit_count INTEGER, last_visit_time INTEGER)");
	const insertHistory = history.prepare("INSERT INTO urls VALUES (?, ?, ?, ?)");
	insertHistory.run("https://github.com/openai", "OpenAI", 4, chromiumMicros("2026-01-01T00:00:00.000Z"));
	insertHistory.run("https://example.com/private", "Filtered", 1, chromiumMicros("2026-01-02T00:00:00.000Z"));
	history.close();
	return { localAppData, profileRoot };
}

async function addChromeProfile(
	localAppData: string,
	directory: string,
	name: string,
	cookieValue = "secondary",
	historyTitle = "Secondary",
): Promise<void> {
	const userData = path.join(localAppData, "Google", "Chrome", "User Data");
	const profileRoot = path.join(userData, directory);
	await mkdir(path.join(profileRoot, "Network"), { recursive: true });
	await writeFile(path.join(userData, "Local State"), JSON.stringify({
		profile: { info_cache: { Default: { name: "Personal" }, [directory]: { name } } },
	}));
	const future = chromiumMicros("2030-01-01T00:00:00.000Z");
	const cookies = new Database(path.join(profileRoot, "Network", "Cookies"));
	cookies.exec(`
		CREATE TABLE cookies (
			host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT,
			expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER,
			top_frame_site_key TEXT, last_access_utc INTEGER, creation_utc INTEGER
		)
	`);
	cookies.prepare("INSERT INTO cookies VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)").run(
		".github.com", "session", cookieValue, Buffer.alloc(0), "/", future, 1, 1, 1, "", future, future,
	);
	cookies.close();
	const history = new Database(path.join(profileRoot, "History"));
	history.exec("CREATE TABLE urls (url TEXT, title TEXT, visit_count INTEGER, last_visit_time INTEGER)");
	history.prepare("INSERT INTO urls VALUES (?, ?, ?, ?)").run(
		"https://github.com/openai",
		historyTitle,
		7,
		chromiumMicros("2026-02-01T00:00:00.000Z"),
	);
	history.close();
}

async function createLiveFirefoxFixture(root: string): Promise<{
	appData: string;
	close: () => void;
}> {
	const appData = path.join(root, "roaming");
	const profileRoot = path.join(appData, "Mozilla", "Firefox", "Profiles", "fixture.default-release");
	await mkdir(profileRoot, { recursive: true });

	const cookies = new Database(path.join(profileRoot, "cookies.sqlite"));
	cookies.pragma("journal_mode = WAL");
	cookies.pragma("wal_autocheckpoint = 0");
	cookies.exec(`
		CREATE TABLE moz_cookies (
			host TEXT, name TEXT, value TEXT, path TEXT, expiry INTEGER,
			isSecure INTEGER, isHttpOnly INTEGER, sameSite INTEGER,
			originAttributes TEXT, lastAccessed INTEGER, creationTime INTEGER
		);
		INSERT INTO moz_cookies VALUES ('.example.com', 'session', 'firefox', '/', 1893456000, 1, 1, 1, '', 2, 1);
		INSERT INTO moz_cookies VALUES ('.container.example', 'container', 'private', '/', 1893456000, 1, 1, 1, '^userContextId=2', 3, 2);
	`);

	const history = new Database(path.join(profileRoot, "places.sqlite"));
	history.pragma("journal_mode = WAL");
	history.pragma("wal_autocheckpoint = 0");
	history.exec(`
		CREATE TABLE moz_places (url TEXT, title TEXT, visit_count INTEGER, last_visit_date INTEGER);
		INSERT INTO moz_places VALUES ('https://example.com/from-firefox', 'Firefox fixture', 3, 1767225600000000);
	`);

	return {
		appData,
		close: () => {
			cookies.close();
			history.close();
		},
	};
}

function chromiumMicros(iso: string): number {
	return Math.round((Date.parse(iso) / 1_000 + 11_644_473_600) * 1_000_000);
}

describe("BrowserProfileImportService", () => {
	it("removes Chromium v24's domain hash from decrypted Windows cookie values", () => {
		const host = ".example.com";
		const key = Buffer.alloc(32, 0x11);
		const nonce = Buffer.alloc(12, 0x22);
		const cipher = createCipheriv("aes-256-gcm", key, nonce);
		const plaintext = Buffer.concat([
			createHash("sha256").update(host).digest(),
			Buffer.from("usable-cookie-value"),
		]);
		const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
		const encrypted = Buffer.concat([Buffer.from("v10"), nonce, ciphertext, cipher.getAuthTag()]);

		expect(decryptWindowsChromiumCookie(encrypted, key, host)).toBe("usable-cookie-value");
		expect(decryptWindowsChromiumCookie(encrypted, key, ".wrong.example")).not.toBe("usable-cookie-value");
	});

	it("imports a transaction-consistent Firefox snapshot while the source databases are open", async () => {
		const root = await fixtureRoot();
		const fixture = await createLiveFirefoxFixture(root);
		try {
			const stateDir = path.join(root, "ao-state");
			const profileStore = new BrowserProfileStore({ stateDir });
			await profileStore.load();
			const historyStore = new BrowserHistoryStore({ stateDir });
			const cookies = vi.fn(async () => undefined);
			const service = new BrowserProfileImportService({
				stateDir,
				profileStore,
				historyStore,
				platform: "win32",
				homeDir: root,
				env: { APPDATA: fixture.appData },
				now: () => new Date("2026-01-01T00:00:00.000Z"),
				fromPartition: () => ({
					cookies: { set: cookies },
					clearStorageData: async () => undefined,
					clearCache: async () => undefined,
				}),
			});
			const source = (await service.discover()).sources[0]!;
			expect(source).toMatchObject({ name: "Firefox", family: "firefox" });

			const result = await service.import({
				requestId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				sourceId: source.id,
				profileIds: [source.profiles[0]!.id],
				includeCookies: true,
				includeHistory: true,
				destination: { mode: "merge", name: "Imported Firefox" },
			}, vi.fn());

			expect(result.entries[0]).toMatchObject({
				importedCookies: 1,
				importedHistoryEntries: 1,
				skippedCookies: 1,
			});
			expect(result.entries[0]!.warnings).toContainEqual({ code: "isolated-cookies-skipped", count: 1 });
			expect(cookies).toHaveBeenCalledWith(expect.objectContaining({
				name: "session",
				value: "firefox",
			}));
		} finally {
			fixture.close();
		}
	});

	it("imports cookies from older Firefox schemas without sameSite", async () => {
		const root = await fixtureRoot();
		const appData = path.join(root, "roaming");
		const profileRoot = path.join(appData, "Mozilla", "Firefox", "Profiles", "legacy.default-release");
		await mkdir(profileRoot, { recursive: true });
		const database = new Database(path.join(profileRoot, "cookies.sqlite"));
		database.exec(`
			CREATE TABLE moz_cookies (
				host TEXT, name TEXT, value TEXT, path TEXT, expiry INTEGER,
				isSecure INTEGER, isHttpOnly INTEGER, originAttributes TEXT,
				lastAccessed INTEGER, creationTime INTEGER
			);
			INSERT INTO moz_cookies VALUES ('.example.com', 'legacy', 'value', '/', 1893456000, 1, 1, '', 2, 1);
		`);
		database.close();
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const setCookie = vi.fn(async () => undefined);
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { APPDATA: appData },
			now: () => new Date("2026-01-01T00:00:00.000Z"),
			fromPartition: () => ({
				cookies: { set: setCookie },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});
		const source = (await service.discover()).sources[0]!;
		const result = await service.import({
			requestId: "abababab-abab-4bab-8bab-abababababab",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: true,
			includeHistory: false,
			destination: { mode: "merge", name: "Legacy Firefox" },
		}, vi.fn());

		expect(setCookie).toHaveBeenCalledWith(expect.objectContaining({ name: "legacy", sameSite: "unspecified" }));
		expect(result.entries[0]!.warnings).toContainEqual({ code: "cookie-attributes-defaulted", count: 1 });
	});

	it("imports Firefox history when a detected profile has an empty cookie database", async () => {
		const root = await fixtureRoot();
		const appData = path.join(root, "roaming");
		const profileRoot = path.join(appData, "Mozilla", "Firefox", "Profiles", "fixture.default-release");
		await mkdir(profileRoot, { recursive: true });
		new Database(path.join(profileRoot, "cookies.sqlite")).close();
		const history = new Database(path.join(profileRoot, "places.sqlite"));
		history.exec(`
			CREATE TABLE moz_places (url TEXT, title TEXT, visit_count INTEGER, last_visit_date INTEGER);
			INSERT INTO moz_places VALUES ('https://example.com/from-firefox', 'Firefox fixture', 3, 1767225600000000);
		`);
		history.close();

		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const setCookie = vi.fn(async () => undefined);
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { APPDATA: appData },
			fromPartition: () => ({
				cookies: { set: setCookie },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});
		const source = (await service.discover()).sources[0]!;
		const result = await service.import({
			requestId: "abababab-abab-4bab-8bab-abababababab",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: true,
			includeHistory: true,
			destination: { mode: "merge", name: "History-only Firefox" },
		}, vi.fn());

		expect(result.entries[0]).toMatchObject({
			importedCookies: 0,
			importedHistoryEntries: 1,
			warnings: [expect.objectContaining({ code: "cookie-database-missing" })],
		});
		expect(setCookie).not.toHaveBeenCalled();
	});

	it("discovers path-hidden profiles and atomically imports supported cookies and history", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const historyStore = new BrowserHistoryStore({ stateDir });
		const cookiesByPartition = new Map<string, unknown[]>();
		const cleared: string[] = [];
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore,
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			now: () => new Date("2026-01-01T00:00:00.000Z"),
			fromPartition: (partition) => ({
				cookies: { set: async (cookie) => { cookiesByPartition.set(partition, [...(cookiesByPartition.get(partition) ?? []), cookie]); } },
				clearStorageData: async () => { cleared.push(partition); },
				clearCache: async () => undefined,
			}),
		});
		const discovery = await service.discover();
		expect(discovery.sources).toHaveLength(1);
		expect(JSON.stringify(discovery)).not.toContain(root);
		const source = discovery.sources[0]!;
		const sourceProfile = source.profiles[0]!;
		const progress = vi.fn();
		const result = await service.import({
			requestId: "22222222-2222-4222-8222-222222222222",
			sourceId: source.id,
			profileIds: [sourceProfile.id],
			includeCookies: true,
			includeHistory: true,
			destination: { mode: "merge", name: "Imported Chrome" },
		}, progress);

		expect(result.entries[0]).toMatchObject({ importedCookies: 3, skippedCookies: 3, importedHistoryEntries: 2 });
		expect(result.entries[0]!.warnings).toEqual(expect.arrayContaining([
			expect.objectContaining({ code: "encrypted-cookies-skipped", count: 1 }),
			expect.objectContaining({ code: "expired-cookies-skipped", count: 1 }),
			expect.objectContaining({ code: "isolated-cookies-skipped", count: 1 }),
		]));
		expect([...cookiesByPartition.values()][0]).toHaveLength(3);
		expect(progress).toHaveBeenLastCalledWith(expect.objectContaining({ phase: "importing", completed: 1, total: 1 }));
		const importedProfile = result.entries[0]!.destinationProfile;
		expect(await new BrowserHistoryStore({ stateDir }).suggest(importedProfile.id, "openai")).toEqual([
			{ url: "https://github.com/openai", title: "OpenAI" },
		]);
		expect((await new BrowserProfileStore({ stateDir }).load()).profiles).toHaveLength(1);

		await expect(service.import({
			requestId: "33333333-3333-4333-8333-333333333333",
			sourceId: source.id,
			profileIds: [sourceProfile.id],
			includeCookies: true,
			includeHistory: false,
			destination: { mode: "merge", name: "Imported Chrome" },
		}, vi.fn())).rejects.toThrow("already exists");
		expect(cleared).toEqual([]);
	});

	it("uses selected-profile order for cookie conflicts and newest metadata for merged history", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		await addChromeProfile(localAppData, "Profile 1", "Work", "secondary", "Newer title");
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const importedCookies: Array<{ name?: string; value?: string }> = [];
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			now: () => new Date("2026-01-01T00:00:00.000Z"),
			fromPartition: () => ({
				cookies: { set: async (cookie) => { importedCookies.push(cookie); } },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});
		const source = (await service.discover()).sources[0]!;
		const result = await service.import({
			requestId: "93939393-9393-4393-8393-939393939393",
			sourceId: source.id,
			profileIds: source.profiles.map((profile) => profile.id),
			includeCookies: true,
			includeHistory: true,
			destination: { mode: "merge", name: "Merged" },
		}, vi.fn());

		expect(importedCookies.find((cookie) => cookie.name === "session")?.value).toBe("usable");
		const profileId = result.entries[0]!.destinationProfile.id;
		const persisted = JSON.parse(await readFile(
			path.join(stateDir, "browser-history", `${profileId}.json`),
			"utf8",
		)) as { entries: Array<{ url: string; title?: string; visitCount: number }> };
		expect(persisted.entries.find((entry) => entry.url === "https://github.com/openai")).toMatchObject({
			title: "Newer title",
			visitCount: 11,
		});
	});

	it("rolls back a failed destination and allows a clean retry", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const historyStore = new BrowserHistoryStore({ stateDir });
		vi.spyOn(historyStore, "mergeImportedEntries").mockRejectedValueOnce(new Error("disk full"));
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore,
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({ cookies: { set: async () => undefined }, clearStorageData: async () => undefined, clearCache: async () => undefined }),
		});
		const source = (await service.discover()).sources[0]!;
		const request = {
			requestId: "44444444-4444-4444-8444-444444444444",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: false,
			includeHistory: true,
			destination: { mode: "merge" as const, name: "Retryable" },
		};
		await expect(service.import(request, vi.fn())).rejects.toThrow("disk full");
		expect(profileStore.profiles).toEqual([]);
		await expect(service.import({ ...request, requestId: "55555555-5555-4555-8555-555555555555" }, vi.fn())).resolves.toMatchObject({
			entries: [expect.objectContaining({ importedHistoryEntries: 2 })],
		});
	});

	it("rejects a separate import before reading when destination capacity is insufficient", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		await addChromeProfile(localAppData, "Profile 1", "Work");
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		for (let index = 0; index < BROWSER_PROFILE_MAX_COUNT - 1; index += 1) {
			await profileStore.createProfile(`Existing ${index + 1}`);
		}
		const fromPartition = vi.fn(() => ({
			cookies: { set: async () => undefined },
			clearStorageData: async () => undefined,
			clearCache: async () => undefined,
		}));
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition,
		});
		const source = (await service.discover()).sources[0]!;

		await expect(service.import({
			requestId: "91919191-9191-4191-8191-919191919191",
			sourceId: source.id,
			profileIds: source.profiles.map((profile) => profile.id),
			includeCookies: true,
			includeHistory: true,
			destination: {
				mode: "separate",
				names: Object.fromEntries(source.profiles.map((profile, index) => [profile.id, `Imported ${index + 1}`])),
			},
		}, vi.fn())).rejects.toThrow("Not enough browser profile slots");
		expect(profileStore.profiles).toHaveLength(BROWSER_PROFILE_MAX_COUNT - 1);
		expect(fromPartition).not.toHaveBeenCalled();
		await expect(stat(path.join(stateDir, "browser-import-staging"))).rejects.toMatchObject({ code: "ENOENT" });
	});

	it("cancels and rolls back an active import before disposal completes", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		let releaseCookie!: () => void;
		const cookieBlocked = new Promise<void>((resolve) => {
			releaseCookie = resolve;
		});
		const setCookie = vi.fn(() => cookieBlocked);
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({
				cookies: { set: setCookie },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});
		const source = (await service.discover()).sources[0]!;
		const importing = service.import({
			requestId: "92929292-9292-4292-8292-929292929292",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: true,
			includeHistory: false,
			destination: { mode: "merge", name: "Interrupted" },
		}, vi.fn());
		await vi.waitFor(() => expect(setCookie).toHaveBeenCalled());

		let disposed = false;
		const disposing = service.dispose().then(() => {
			disposed = true;
		});
		await Promise.resolve();
		expect(disposed).toBe(false);
		releaseCookie();

		await expect(importing).rejects.toThrow("app is closing");
		await disposing;
		expect(profileStore.profiles).toEqual([]);
		await expect(stat(path.join(stateDir, "browser-import-staging"))).rejects.toMatchObject({ code: "ENOENT" });
	});

	it("removes stale import staging during initialization", async () => {
		const root = await fixtureRoot();
		const stateDir = path.join(root, "ao-state");
		const stale = path.join(stateDir, "browser-import-staging", "stale", "snapshot.sqlite");
		await mkdir(path.dirname(stale), { recursive: true });
		await writeFile(stale, "partial");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			fromPartition: () => ({
				cookies: { set: async () => undefined },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});

		await service.initialize();
		await expect(stat(path.join(stateDir, "browser-import-staging"))).rejects.toMatchObject({ code: "ENOENT" });
	});

	it("retains a visible destination when Electron session cleanup cannot start", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		let sessionAvailable = false;
		const fromPartition = () => {
			if (!sessionAvailable) throw new Error("session unavailable");
			return { cookies: { set: async () => undefined }, clearStorageData: async () => undefined, clearCache: async () => undefined };
		};
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition,
		});
		const source = (await service.discover()).sources[0]!;
		const request = {
			requestId: "88888888-8888-4888-8888-888888888888",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: true,
			includeHistory: false,
			destination: { mode: "merge" as const, name: "Retry Session" },
		};

		await expect(service.import(request, vi.fn())).rejects.toThrow("remain in Browser settings");
		expect(profileStore.profiles).toEqual([expect.objectContaining({ name: "Retry Session" })]);
		sessionAvailable = true;
		const retained = profileStore.profiles[0]!;
		const session = fromPartition();
		await session.clearStorageData();
		await session.clearCache();
		await profileStore.deleteProfile(retained.id);
		expect(profileStore.profiles).toEqual([]);
	});

	it("keeps a failed destination registered when rollback cannot remove all imported data", async () => {
		const root = await fixtureRoot();
		const { localAppData } = await createChromeFixture(root);
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const historyStore = new BrowserHistoryStore({ stateDir });
		vi.spyOn(historyStore, "mergeImportedEntries").mockRejectedValueOnce(new Error("disk full"));
		vi.spyOn(historyStore, "clear").mockRejectedValueOnce(new Error("cleanup failed"));
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore,
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({
				cookies: { set: async () => undefined },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});
		const source = (await service.discover()).sources[0]!;

		await expect(service.import({
			requestId: "12121212-1212-4212-8212-121212121212",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: false,
			includeHistory: true,
			destination: { mode: "merge", name: "Cleanup retry" },
		}, vi.fn())).rejects.toThrow("remain in Browser settings");
		expect(profileStore.profiles).toEqual([expect.objectContaining({ name: "Cleanup retry" })]);
	});

	it("reports the exact deterministic history truncation count", async () => {
		const root = await fixtureRoot();
		const { localAppData, profileRoot } = await createChromeFixture(root);
		const history = new Database(path.join(profileRoot, "History"));
		const insert = history.prepare("INSERT INTO urls VALUES (?, ?, ?, ?)");
		history.exec("BEGIN");
		for (let index = 0; index < 5_000; index += 1) {
			insert.run(
				`https://limit.example/${index}`,
				`Limit ${index}`,
				1,
				chromiumMicros(new Date(Date.UTC(2027, 0, 1, 0, 0, index)).toISOString()),
			);
		}
		history.exec("COMMIT");
		history.close();

		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({ cookies: { set: async () => undefined }, clearStorageData: async () => undefined, clearCache: async () => undefined }),
		});
		const source = (await service.discover()).sources[0]!;
		const result = await service.import({
			requestId: "13131313-1313-4313-8313-131313131313",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: false,
			includeHistory: true,
			destination: { mode: "merge", name: "History limit" },
		}, vi.fn());

		expect(result.entries[0]).toMatchObject({ importedHistoryEntries: 5_000 });
		expect(result.entries[0]!.warnings).toContainEqual({ code: "history-limit-truncated", count: 2 });
	});

	it("reports the exact cookie truncation count without importing partitioned cookies", async () => {
		const root = await fixtureRoot();
		const { localAppData, profileRoot } = await createChromeFixture(root);
		const database = new Database(path.join(profileRoot, "Network", "Cookies"));
		const future = chromiumMicros("2030-01-01T00:00:00.000Z");
		const insert = database.prepare("INSERT INTO cookies VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)");
		database.exec("BEGIN");
		for (let index = 0; index < 20_000; index += 1) {
			insert.run(
				".limit.example",
				`cookie-${index}`,
				`value-${index}`,
				Buffer.alloc(0),
				"/",
				future,
				1,
				1,
				1,
				"",
				future + 100 + index,
				future + 100 + index,
			);
		}
		database.exec("COMMIT");
		database.close();

		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		let importedCookies = 0;
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({
				cookies: { set: async () => { importedCookies += 1; } },
				clearStorageData: async () => undefined,
				clearCache: async () => undefined,
			}),
		});
		const source = (await service.discover()).sources[0]!;
		const result = await service.import({
			requestId: "14141414-1414-4414-8414-141414141414",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: true,
			includeHistory: false,
			destination: { mode: "merge", name: "Cookie limit" },
		}, vi.fn());

		expect(importedCookies).toBe(20_000);
		expect(result.entries[0]).toMatchObject({ importedCookies: 20_000, skippedCookies: 6 });
		expect(result.entries[0]!.warnings).toEqual(expect.arrayContaining([
			{ code: "isolated-cookies-skipped", count: 1 },
			{ code: "cookie-limit-truncated", count: 5 },
		]));
	});

	it("rejects an oversized database before reading it", async () => {
		const root = await fixtureRoot();
		const localAppData = path.join(root, "local");
		const profileRoot = path.join(localAppData, "Google", "Chrome", "User Data", "Default");
		await mkdir(profileRoot, { recursive: true });
		await writeFile(path.join(localAppData, "Google", "Chrome", "User Data", "Local State"), "{}");
		const historyFile = path.join(profileRoot, "History");
		await writeFile(historyFile, "");
		await truncate(historyFile, 256 * 1024 * 1024 + 1);
		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({ cookies: { set: async () => undefined }, clearStorageData: async () => undefined, clearCache: async () => undefined }),
		});
		const source = (await service.discover()).sources[0]!;
		await expect(service.import({
			requestId: "66666666-6666-4666-8666-666666666666",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: false,
			includeHistory: true,
			destination: { mode: "merge", name: "Too Large" },
		}, vi.fn())).rejects.toThrow("size limit");
		expect(profileStore.profiles).toEqual([]);
	});

	it("does not follow a source database symlink outside the browser profile", async () => {
		const root = await fixtureRoot();
		const localAppData = path.join(root, "local");
		const userData = path.join(localAppData, "Google", "Chrome", "User Data");
		const profileRoot = path.join(userData, "Default");
		await mkdir(path.join(profileRoot, "Network"), { recursive: true });
		await writeFile(path.join(userData, "Local State"), "{}");
		await writeFile(path.join(profileRoot, "Network", "Cookies"), "");
		const outside = path.join(root, "outside-history.sqlite");
		await writeFile(outside, "not a browser database");
		try {
			await symlink(outside, path.join(profileRoot, "History"), "file");
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code === "EPERM") return;
			throw error;
		}

		const stateDir = path.join(root, "ao-state");
		const profileStore = new BrowserProfileStore({ stateDir });
		await profileStore.load();
		const service = new BrowserProfileImportService({
			stateDir,
			profileStore,
			historyStore: new BrowserHistoryStore({ stateDir }),
			platform: "win32",
			homeDir: root,
			env: { LOCALAPPDATA: localAppData },
			fromPartition: () => ({ cookies: { set: async () => undefined }, clearStorageData: async () => undefined, clearCache: async () => undefined }),
		});
		const source = (await service.discover()).sources[0]!;
		const result = await service.import({
			requestId: "77777777-7777-4777-8777-777777777777",
			sourceId: source.id,
			profileIds: [source.profiles[0]!.id],
			includeCookies: false,
			includeHistory: true,
			destination: { mode: "merge", name: "Contained" },
		}, vi.fn());

		expect(result.entries[0]).toMatchObject({
			importedHistoryEntries: 0,
			warnings: [expect.objectContaining({ code: "history-database-missing" })],
		});
	});
});
