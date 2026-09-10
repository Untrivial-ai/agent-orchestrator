import { chmod, lstat, mkdtemp, readFile, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { TelemetryPolicyAuthority, nodeTelemetryPolicyFileSystem, resolveDesktopDataDir } from "./telemetry-policy-file";

const dirs: string[] = [];
const makeDir = async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-policy-"));
	dirs.push(dir);
	return dir;
};

afterEach(async () => {
	const { rm } = await import("node:fs/promises");
	await Promise.all(dirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("TelemetryPolicyAuthority", () => {
	it("materializes the packaged default as an owner-only durable record", async () => {
		const dataDir = await makeDir();
		const authority = new TelemetryPolicyAuthority({ dataDir, packagedDefault: true, platform: "linux" });
		const snapshot = await authority.load();
		const policyPath = path.join(dataDir, "telemetry_policy.json");
		const mode = (await lstat(policyPath)).mode & 0o777;
		expect(snapshot.eventsEnabled).toBe(true);
		expect(snapshot.acknowledged).toBe(true);
		expect(mode).toBe(0o600);
		expect(JSON.parse(await readFile(policyPath, "utf8"))).toEqual({
			schema_version: 1,
			events_enabled: true,
			consent_generation: snapshot.consentGeneration,
			updated_at: snapshot.updatedAt,
		});
	});

	it.each(["symlink", "group-readable", "malformed"])("fails closed without replacing an unsafe %s authority", async (kind) => {
		const dataDir = await makeDir();
		const policyPath = path.join(dataDir, "telemetry_policy.json");
		if (kind === "symlink") {
			const target = path.join(dataDir, "target.json");
			await writeFile(target, "{}", { mode: 0o600 });
			await symlink(target, policyPath);
		} else {
			await writeFile(policyPath, kind === "malformed" ? "not-json" : JSON.stringify({
				schema_version: 1,
				events_enabled: true,
				consent_generation: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca",
				updated_at: "2026-08-28T10:15:30.000Z",
			}), { mode: 0o600 });
			if (kind === "group-readable") await chmod(policyPath, 0o640);
		}
		const before = await readFile(policyPath, "utf8");
		const authority = new TelemetryPolicyAuthority({ dataDir, packagedDefault: true, platform: "linux" });
		const snapshot = await authority.load();
		expect(snapshot.eventsEnabled).toBe(false);
		expect(snapshot.acknowledged).toBe(false);
		expect(await readFile(policyPath, "utf8")).toBe(before);
	});

	it("does not acknowledge a replace until the containing directory is synced", async () => {
		const dataDir = await makeDir();
		const base = nodeTelemetryPolicyFileSystem;
		const authority = new TelemetryPolicyAuthority({
			dataDir,
			packagedDefault: false,
			platform: "linux",
			fs: { ...base, syncDirectory: async () => { throw new Error("directory fsync failed"); } },
		});
		await expect(authority.load()).rejects.toThrow("directory fsync failed");
		expect(authority.snapshot().eventsEnabled).toBe(false);
		expect(authority.snapshot().acknowledged).toBe(false);
	});

	it("retries an unacknowledged replacement without changing its generation", async () => {
		const dataDir = await makeDir();
		const base = nodeTelemetryPolicyFileSystem;
		let syncAttempts = 0;
		const authority = new TelemetryPolicyAuthority({
			dataDir,
			packagedDefault: false,
			platform: "linux",
			fs: {
				...base,
				syncDirectory: async (target) => {
					syncAttempts++;
					if (syncAttempts === 1) throw new Error("directory fsync failed");
					await base.syncDirectory(target);
				},
			},
		});
		await expect(authority.load()).rejects.toThrow("directory fsync failed");
		const pending = authority.snapshot();
		expect(pending.acknowledged).toBe(false);

		const retried = await authority.retryPendingReplacement();

		expect(retried).toMatchObject({
			eventsEnabled: pending.eventsEnabled,
			consentGeneration: pending.consentGeneration,
			updatedAt: pending.updatedAt,
			acknowledged: true,
		});
		expect(syncAttempts).toBe(2);
	});

	it("keeps Windows fail closed until a write-through replacement helper exists", async () => {
		const dataDir = await makeDir();
		const authority = new TelemetryPolicyAuthority({ dataDir, packagedDefault: true, platform: "win32" });
		const snapshot = await authority.load();
		expect(snapshot).toMatchObject({ eventsEnabled: false, acknowledged: false });
		await expect(authority.setEventsEnabled(true)).rejects.toThrow("unsupported on Windows");
	});
});

it("resolves one absolute data directory against the daemon launch cwd", () => {
	expect(resolveDesktopDataDir({ AO_DATA_DIR: "relative-data" }, "/home/ao", "/work/checkout", false)).toBe("/work/checkout/relative-data");
	expect(resolveDesktopDataDir({}, "/home/ao", "/work/checkout", true)).toBe("/home/ao/.ao/data");
	expect(resolveDesktopDataDir({}, "/home/ao", "/work/checkout", false)).toBe("/home/ao/.ao/dev/data");
});
