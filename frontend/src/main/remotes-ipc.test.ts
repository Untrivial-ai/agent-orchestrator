import { describe, expect, it, vi } from "vitest";
import { chmod, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findRemote, removeSavedRemote, toHostViews, updateSavedRemote } from "./remotes-ipc";
import type { RemoteEntry } from "./remotes-store";

const TWO_HOSTS =
	'{"remotes":[{"label":"workbox","url":"http://192.0.2.1:1","password":"old"},{"label":"mini","url":"http://192.0.2.9:9","password":"m"}]}';

async function tempFile(contents = TWO_HOSTS, mode = 0o600): Promise<string> {
	const dir = await mkdtemp(join(tmpdir(), "ao-remotes-ipc-"));
	const path = join(dir, "remotes.json");
	await writeFile(path, contents, "utf8");
	await chmod(path, mode);
	return path;
}

const online = async () => "online" as const;
const dropped = () => vi.fn<(url: string) => Promise<void>>().mockResolvedValue(undefined);

describe("toHostViews", () => {
	it("strips the password before anything crosses to the renderer", () => {
		const views = toHostViews([{ label: "workbox", url: "http://192.0.2.1:3011", password: "supersecret" }]);
		expect(views).toEqual([{ label: "workbox", url: "http://192.0.2.1:3011" }]);
		expect(JSON.stringify(views)).not.toContain("supersecret");
	});
});

describe("findRemote", () => {
	it("returns the saved entry for a url", async () => {
		const path = await tempFile();
		await expect(findRemote(path, "http://192.0.2.9:9")).resolves.toEqual({ label: "mini", url: "http://192.0.2.9:9", password: "m" });
	});

	it("refuses a url it has never saved", async () => {
		const path = await tempFile();
		await expect(findRemote(path, "http://192.0.2.5:5")).rejects.toThrow(/no saved host/);
	});
});

describe("updateSavedRemote", () => {
	it("probes the merged entry before it writes anything", async () => {
		const path = await tempFile();
		const probed: RemoteEntry[] = [];
		const health = await updateSavedRemote(path, "http://192.0.2.1:1", { password: "rotated" }, dropped(), async (entry) => {
			probed.push(entry);
			return "online";
		});
		expect(health).toBe("online");
		// Probed with the new password against the saved address, not with either half.
		expect(probed).toEqual([{ label: "workbox", url: "http://192.0.2.1:1", password: "rotated" }]);
	});

	it("saves nothing and drops nothing when the edited host does not answer", async () => {
		const path = await tempFile();
		const disconnect = dropped();
		const health = await updateSavedRemote(path, "http://192.0.2.1:1", { password: "wrong" }, disconnect, async () => "unauthorized");
		expect(health).toBe("unauthorized");
		expect(await readFile(path, "utf8")).toBe(TWO_HOSTS);
		expect(disconnect).not.toHaveBeenCalled();
	});

	// A live proxy holds the address and password that were saved when it
	// started; after an edit both may be stale, so it does not get to keep serving.
	it("drops the edited host's proxy by its old url", async () => {
		const path = await tempFile();
		const disconnect = dropped();
		await updateSavedRemote(path, "http://192.0.2.1:1", { url: "http://192.0.2.5:5" }, disconnect, online);
		expect(disconnect).toHaveBeenCalledTimes(1);
		expect(disconnect).toHaveBeenCalledWith("http://192.0.2.1:1");
	});
});

describe("removeSavedRemote", () => {
	it("forgets the host and drops its proxy", async () => {
		const path = await tempFile();
		const disconnect = dropped();
		await removeSavedRemote(path, "http://192.0.2.1:1", disconnect);
		expect(JSON.parse(await readFile(path, "utf8")).remotes).toEqual([{ label: "mini", url: "http://192.0.2.9:9", password: "m" }]);
		expect(disconnect).toHaveBeenCalledWith("http://192.0.2.1:1");
	});

	it("refuses to touch a file others can read", async () => {
		const path = await tempFile(TWO_HOSTS, 0o644);
		const disconnect = dropped();
		await expect(removeSavedRemote(path, "http://192.0.2.1:1", disconnect)).rejects.toThrow(/chmod 600/);
		expect(disconnect).not.toHaveBeenCalled();
	});
});
