import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
	BROWSER_PROFILE_MAX_COUNT,
	BROWSER_PROFILE_REGISTRY_VERSION,
	browserProfilePartition,
	type BrowserProfileRegistry,
} from "../shared/browser-profiles";
import { BrowserProfileStore } from "./browser-profile-store";

const tempDirectories: string[] = [];

async function makeStateDir(): Promise<string> {
	const directory = await mkdtemp(path.join(os.tmpdir(), "ao-browser-profiles-"));
	tempDirectories.push(directory);
	return directory;
}

function profileId(index: number): string {
	return `00000000-0000-4000-8000-${index.toString(16).padStart(12, "0")}`;
}

function registryWithProfiles(count: number): BrowserProfileRegistry {
	const now = "2026-01-01T00:00:00.000Z";
	return {
		version: BROWSER_PROFILE_REGISTRY_VERSION,
		profiles: Array.from({ length: count }, (_, index) => ({
			id: profileId(index + 1),
			name: `Profile ${index + 1}`,
			createdAt: now,
			updatedAt: now,
		})),
		bindings: {},
	};
}

afterEach(async () => {
	await Promise.all(tempDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })));
});

describe("BrowserProfileStore", () => {
	it("treats a missing registry as empty and persists named profiles atomically", async () => {
		const stateDir = await makeStateDir();
		const store = new BrowserProfileStore({ stateDir });

		expect(await store.load()).toEqual({ profiles: [] });
		const work = await store.createProfile(" Work ");
		const personal = await store.createProfile("Personal");

		expect(work.name).toBe("Work");
		expect(store.profiles.map((profile) => profile.name)).toEqual(["Work", "Personal"]);
		const persisted = JSON.parse(await readFile(path.join(stateDir, "browser-profiles.json"), "utf8")) as BrowserProfileRegistry;
		expect(persisted.version).toBe(BROWSER_PROFILE_REGISTRY_VERSION);
		expect(persisted.profiles).toHaveLength(2);
		expect(await readdir(stateDir)).not.toContain(expect.stringContaining(".browser-profiles.json."));

		const reconstructed = new BrowserProfileStore({ stateDir });
		await reconstructed.load();
		expect(reconstructed.getProfile(work.id)?.name).toBe("Work");
		await reconstructed.bindSession("worker-1", personal.id);
		expect(reconstructed.getSessionProfileId("worker-1")).toBe(personal.id);

		const reloaded = new BrowserProfileStore({ stateDir });
		await reloaded.load();
		expect(reloaded.getSessionProfileId("worker-1")).toBe(personal.id);
	});

	it("serializes concurrent mutations without losing profiles", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });

		await Promise.all([store.createProfile("Work"), store.createProfile("Personal"), store.createProfile("Research")]);

		expect(new Set(store.profiles.map((profile) => profile.name))).toEqual(new Set(["Work", "Personal", "Research"]));
	});

	it("validates names, rejects duplicate names, and keeps partitions ID-derived", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const profile = await store.createProfile("Work");

		await expect(store.createProfile(" work ")).rejects.toMatchObject({ code: "BROWSER_PROFILE_NAME_TAKEN" });
		await expect(store.createProfile("\u0000bad")).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		await expect(store.renameProfile("not-a-uuid", "Other")).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		await expect(store.bindSession("__proto__", profile.id)).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
		await expect(store.bindSession("toString", profile.id)).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });

		const partition = browserProfilePartition(profile.id);
		await store.renameProfile(profile.id, "Personal");
		expect(browserProfilePartition(profile.id)).toBe(partition);
		expect(partition).toBe(`persist:ao-browser-profile-${profile.id}`);
	});

	it("preserves corrupt registries and surfaces a recoverable error", async () => {
		const stateDir = await makeStateDir();
		const registryPath = path.join(stateDir, "browser-profiles.json");
		const corrupt = JSON.stringify({ version: 99, profiles: [], bindings: {} });
		await writeFile(registryPath, corrupt, "utf8");
		const store = new BrowserProfileStore({ stateDir });

		expect((await store.load()).error).toMatchObject({ code: "BROWSER_PROFILE_STORE_CORRUPT" });
		await expect(store.createProfile("Work")).rejects.toMatchObject({ code: "BROWSER_PROFILE_STORE_CORRUPT" });
		expect(await readFile(registryPath, "utf8")).toBe(corrupt);
	});

	it("rejects duplicate or over-capacity registry records", async () => {
		const stateDir = await makeStateDir();
		const registryPath = path.join(stateDir, "browser-profiles.json");
		const duplicate = registryWithProfiles(2);
		duplicate.profiles[1]!.name = duplicate.profiles[0]!.name;
		await writeFile(registryPath, JSON.stringify(duplicate), "utf8");
		const duplicateStore = new BrowserProfileStore({ stateDir });
		expect((await duplicateStore.load()).error?.code).toBe("BROWSER_PROFILE_STORE_CORRUPT");

		const cappedStateDir = await makeStateDir();
		await writeFile(path.join(cappedStateDir, "browser-profiles.json"), JSON.stringify(registryWithProfiles(BROWSER_PROFILE_MAX_COUNT + 1)), "utf8");
		const cappedStore = new BrowserProfileStore({ stateDir: cappedStateDir });
		expect((await cappedStore.load()).error?.code).toBe("BROWSER_PROFILE_STORE_CORRUPT");
	});

	it("refuses destructive operations while a profile is live and deletes stale bindings after cleanup", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const profile = await store.createProfile("Work");
		await store.bindSession("worker-1", profile.id);
		const operation = async () => "cleared";

		await expect(store.runProfileOperation(profile.id, () => true, operation)).rejects.toMatchObject({
			code: "BROWSER_PROFILE_ACTIVE",
		});
		expect(store.isProfileOperationInProgress(profile.id)).toBe(false);

		await store.runProfileOperation(profile.id, () => false, async () => {
			await store.deleteProfile(profile.id);
		});
		expect(store.getProfile(profile.id)).toBeUndefined();
		expect(store.getSessionProfileId("worker-1")).toBeUndefined();
	});

	it("serializes profile data operations and keeps the live-operation marker until the full queue drains", async () => {
		const store = new BrowserProfileStore({ stateDir: await makeStateDir() });
		const profile = await store.createProfile("Work");
		let releaseFirst!: () => void;
		let releaseSecond!: () => void;
		const firstHeld = new Promise<void>((resolve) => {
			releaseFirst = resolve;
		});
		const secondHeld = new Promise<void>((resolve) => {
			releaseSecond = resolve;
		});
		const first = store.runProfileOperation(profile.id, () => false, async () => {
			await firstHeld;
			return "first";
		});
		const second = store.runProfileOperation(profile.id, () => false, async () => {
			await secondHeld;
			return "second";
		});
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(store.isProfileOperationInProgress(profile.id)).toBe(true);
		releaseFirst();
		expect(await first).toBe("first");
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(store.isProfileOperationInProgress(profile.id)).toBe(true);
		let queueDrained = false;
		const drain = store.waitForProfileOperation(profile.id).then(() => {
			queueDrained = true;
		});
		await new Promise<void>((resolve) => setImmediate(resolve));
		expect(queueDrained).toBe(false);
		releaseSecond();
		expect(await second).toBe("second");
		await drain;
		expect(store.isProfileOperationInProgress(profile.id)).toBe(false);
	});
});
