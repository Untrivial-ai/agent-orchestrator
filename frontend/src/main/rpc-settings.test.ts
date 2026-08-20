// @vitest-environment node
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdtemp, rm, writeFile, readdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	readRpcSettings,
	updateRpcSettings,
	writeRpcSettings,
} from "./rpc-settings";
import { RPC_SETTINGS_FILE_NAME } from "../shared/rpc";

describe("rpc-settings", () => {
	let dir: string;
	beforeEach(async () => {
		dir = await mkdtemp(path.join(os.tmpdir(), "ao-rpc-settings-"));
	});
	afterEach(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	it("returns safe defaults when no file exists", async () => {
		expect(await readRpcSettings(dir)).toEqual({ enabled: false });
	});

	it("round-trips written settings", async () => {
		await writeRpcSettings(dir, { enabled: true });
		expect(await readRpcSettings(dir)).toEqual({ enabled: true });
	});

	it("falls back to defaults on garbage", async () => {
		await writeFile(path.join(dir, RPC_SETTINGS_FILE_NAME), "{not json", "utf8");
		expect(await readRpcSettings(dir)).toEqual({ enabled: false });
	});

	it("coerces a non-boolean enabled back to false", async () => {
		await writeFile(
			path.join(dir, RPC_SETTINGS_FILE_NAME),
			JSON.stringify({ enabled: "yes" }),
			"utf8",
		);
		expect((await readRpcSettings(dir)).enabled).toBe(false);
	});

	it("coerces a null file content back to defaults", async () => {
		await writeFile(
			path.join(dir, RPC_SETTINGS_FILE_NAME),
			JSON.stringify(null),
			"utf8",
		);
		expect(await readRpcSettings(dir)).toEqual({ enabled: false });
	});

	it("atomic write leaves no temp file behind", async () => {
		await writeRpcSettings(dir, { enabled: true });
		const entries = await readdir(dir);
		expect(entries).toEqual([RPC_SETTINGS_FILE_NAME]);
	});

	it("serializes a read-modify-write with later settings writes", async () => {
		await writeRpcSettings(dir, { enabled: false });
		let releaseMutation!: () => void;
		const mutationBlocked = new Promise<void>((resolve) => {
			releaseMutation = resolve;
		});
		let mutationStarted!: () => void;
		const started = new Promise<void>((resolve) => {
			mutationStarted = resolve;
		});

		const mutation = updateRpcSettings(dir, async (current) => {
			mutationStarted();
			await mutationBlocked;
			return { ...current, enabled: true };
		});
		await started;
		const newer = { enabled: false };
		const laterWrite = writeRpcSettings(dir, newer);

		releaseMutation();
		await Promise.all([mutation, laterWrite]);

		expect(await readRpcSettings(dir)).toEqual(newer);
	});
});
