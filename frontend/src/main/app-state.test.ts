// @vitest-environment node
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	APP_STATE_FILE_NAME,
	writeAppStateMarker,
	readMigrationState,
	updateMigration,
	type AppStateMarker,
} from "./app-state";

// The exact key set the Go reader (start.go `appState`) unmarshals.
const GO_READER_KEYS = ["schemaVersion", "appPath", "version", "installedAt", "lastReconciledAt", "installSource"];

async function readMarker(dir: string): Promise<AppStateMarker> {
	const raw = await readFile(path.join(dir, APP_STATE_FILE_NAME), "utf8");
	return JSON.parse(raw) as AppStateMarker;
}

describe("writeAppStateMarker", () => {
	let dir: string;

	beforeEach(async () => {
		dir = await mkdtemp(path.join(os.tmpdir(), "ao-app-state-"));
	});

	afterEach(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("first write sets installedAt + installSource from installedVia", async () => {
		const t = new Date("2026-06-26T10:00:00.000Z");
		await writeAppStateMarker({
			stateDir: dir,
			appPath: "/Applications/Agent Orchestrator.app",
			version: "0.0.0",
			installedVia: "npm-bootstrap",
			now: () => t,
		});

		const m = await readMarker(dir);
		expect(m.schemaVersion).toBe(2);
		expect(m.appPath).toBe("/Applications/Agent Orchestrator.app");
		expect(m.version).toBe("0.0.0");
		expect(m.installedAt).toBe("2026-06-26T10:00:00.000Z");
		expect(m.lastReconciledAt).toBe("2026-06-26T10:00:00.000Z");
		expect(m.installSource).toBe("npm-bootstrap");
	});

	it("records the current build's restart protocol without preserving a newer build's capability", async () => {
		const options = { stateDir: dir, appPath: "/Applications/AO.app", version: "1.2.3", now: () => new Date() };
		await writeAppStateMarker({ ...options, updateRestartProtocol: 1 });
		expect((await readMarker(dir)).updateRestartProtocol).toBe(1);
		// An older marker writer reconstructs the object and drops unknown fields.
		await writeAppStateMarker(options);
		expect((await readMarker(dir)).updateRestartProtocol).toBeUndefined();
	});

	it("second write PRESERVES installedAt/installSource and updates appPath/version/lastReconciledAt", async () => {
		await writeAppStateMarker({
			stateDir: dir,
			appPath: "/tmp/staging/Agent Orchestrator.app",
			version: "0.0.0",
			installedVia: "npm-bootstrap",
			now: () => new Date("2026-06-26T10:00:00.000Z"),
		});

		// Second launch: app relocated, version bumped, different install arg.
		await writeAppStateMarker({
			stateDir: dir,
			appPath: "/Applications/Agent Orchestrator.app",
			version: "1.2.3",
			installedVia: "github",
			now: () => new Date("2026-06-26T11:30:00.000Z"),
		});

		const m = await readMarker(dir);
		// Preserved from first creation.
		expect(m.installedAt).toBe("2026-06-26T10:00:00.000Z");
		expect(m.installSource).toBe("npm-bootstrap");
		// Refreshed.
		expect(m.appPath).toBe("/Applications/Agent Orchestrator.app");
		expect(m.version).toBe("1.2.3");
		expect(m.lastReconciledAt).toBe("2026-06-26T11:30:00.000Z");
	});

	it("written JSON keys exactly match the Go reader struct", async () => {
		await writeAppStateMarker({
			stateDir: dir,
			appPath: "/Applications/Agent Orchestrator.app",
			version: "0.0.0",
			installedVia: "npm-bootstrap",
			now: () => new Date("2026-06-26T10:00:00.000Z"),
		});

		const raw = await readFile(path.join(dir, APP_STATE_FILE_NAME), "utf8");
		const keys = Object.keys(JSON.parse(raw) as Record<string, unknown>);
		expect(keys.sort()).toEqual([...GO_READER_KEYS].sort());
		// Trailing newline, mirroring runfile.Write.
		expect(raw.endsWith("}\n")).toBe(true);
	});

	it("installedVia undefined => installSource 'unknown'", async () => {
		await writeAppStateMarker({
			stateDir: dir,
			appPath: "/Applications/Agent Orchestrator.app",
			version: "0.0.0",
			now: () => new Date("2026-06-26T10:00:00.000Z"),
		});

		const m = await readMarker(dir);
		expect(m.installSource).toBe("unknown");
	});

	it("atomic write leaves no temp file behind", async () => {
		await writeAppStateMarker({
			stateDir: dir,
			appPath: "/Applications/Agent Orchestrator.app",
			version: "0.0.0",
			installedVia: "npm-bootstrap",
			now: () => new Date("2026-06-26T10:00:00.000Z"),
		});

		const entries = await readdir(dir);
		expect(entries).toEqual([APP_STATE_FILE_NAME]);
		expect(entries.some((e) => e.startsWith(".app-state-"))).toBe(false);
	});

	it("creates the state dir when it does not exist", async () => {
		const nested = path.join(dir, "does", "not", "exist");
		await writeAppStateMarker({
			stateDir: nested,
			appPath: "/Applications/Agent Orchestrator.app",
			version: "0.0.0",
			now: () => new Date("2026-06-26T10:00:00.000Z"),
		});

		const m = await readMarker(nested);
		expect(m.appPath).toBe("/Applications/Agent Orchestrator.app");
	});
});

// ---- migration marker tests (B1) ----

const fixedNow = () => new Date("2026-06-26T10:00:00.000Z");

describe("migration marker", () => {
	const dirs: string[] = [];
	async function tmp() {
		const dir = await mkdtemp(path.join(os.tmpdir(), "ao-appstate-"));
		dirs.push(dir);
		return dir;
	}

	afterEach(async () => {
		await Promise.all(dirs.splice(0).map((d) => rm(d, { recursive: true, force: true })));
	});

	it("readMigrationState defaults to pending when the file is absent", async () => {
		expect(await readMigrationState(await tmp())).toEqual({ status: "pending" });
	});

	it("readMigrationState defaults to pending when the file is corrupt", async () => {
		const dir = await tmp();
		await writeFile(path.join(dir, APP_STATE_FILE_NAME), "{ not valid json", "utf8");
		expect(await readMigrationState(dir)).toEqual({ status: "pending" });
	});

	it("updateMigration persists status without an existing marker", async () => {
		const dir = await tmp();
		await updateMigration({ stateDir: dir, migration: { status: "declined" }, now: fixedNow });
		expect((await readMigrationState(dir)).status).toBe("declined");
	});

	it("a launch write preserves an existing migration block", async () => {
		const dir = await tmp();
		await updateMigration({ stateDir: dir, migration: { status: "completed" }, now: fixedNow });
		await writeAppStateMarker({ stateDir: dir, appPath: "/A.app", version: "1.2.3", now: fixedNow });
		const raw = JSON.parse(await readFile(path.join(dir, APP_STATE_FILE_NAME), "utf8"));
		expect(raw.schemaVersion).toBe(2);
		expect(raw.appPath).toBe("/A.app");
		expect(raw.migration.status).toBe("completed");
	});

	it("updateMigration does not clobber launch fields", async () => {
		const dir = await tmp();
		await writeAppStateMarker({ stateDir: dir, appPath: "/A.app", version: "1.2.3", now: fixedNow });
		await updateMigration({ stateDir: dir, migration: { status: "failed", error: "x" }, now: fixedNow });
		const raw = JSON.parse(await readFile(path.join(dir, APP_STATE_FILE_NAME), "utf8"));
		expect(raw.appPath).toBe("/A.app");
		expect(raw.migration).toEqual({ status: "failed", error: "x" });
	});
});
