import { describe, expect, it } from "vitest";
import { chmod, mkdtemp, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { addRemote, readRemotes, removeRemote, RemotesFilePermissionError, updateRemote } from "./remotes-store";

async function tempFile(contents?: string, mode = 0o600): Promise<string> {
	const dir = await mkdtemp(join(tmpdir(), "ao-remotes-"));
	const path = join(dir, "remotes.json");
	if (contents !== undefined) {
		await writeFile(path, contents, "utf8");
		await chmod(path, mode);
	}
	return path;
}

describe("readRemotes", () => {
	it("returns an empty list when the file does not exist", async () => {
		const path = await tempFile();
		await expect(readRemotes(path)).resolves.toEqual([]);
	});

	it("reads entries from a 0600 file", async () => {
		const path = await tempFile('{"remotes":[{"label":"workbox","url":"http://192.0.2.1:3011","password":"pw"}]}');
		await expect(readRemotes(path)).resolves.toEqual([
			{ label: "workbox", url: "http://192.0.2.1:3011", password: "pw" },
		]);
	});

	it("refuses a file readable by others, naming the fix", async () => {
		const path = await tempFile('{"remotes":[]}', 0o644);
		await expect(readRemotes(path)).rejects.toBeInstanceOf(RemotesFilePermissionError);
		await expect(readRemotes(path)).rejects.toThrow(/chmod 600/);
	});

	// Node reports 0o666 for every writable file on Windows, so the mode check
	// there refuses a perfectly good file and takes saved hosts down with it.
	// The CLI exempts win32 for the same reason (cli/remote.go:154).
	it("does not apply the mode check on windows", async () => {
		const path = await tempFile('{"remotes":[]}', 0o644);
		const platform = Object.getOwnPropertyDescriptor(process, "platform");
		Object.defineProperty(process, "platform", { value: "win32", configurable: true });
		try {
			await expect(readRemotes(path)).resolves.toEqual([]);
		} finally {
			if (platform) Object.defineProperty(process, "platform", platform);
		}
	});
});

describe("addRemote", () => {
	it("creates the file 0600 when absent", async () => {
		const path = await tempFile();
		await addRemote(path, { label: "workbox", url: "http://192.0.2.1:3011", password: "pw" });
		expect((await stat(path)).mode & 0o777).toBe(0o600);
		expect(JSON.parse(await readFile(path, "utf8")).remotes).toHaveLength(1);
		expect(await readdir(dirname(path))).toEqual(["remotes.json"]);
	});

	it("keeps both hosts when adds overlap", async () => {
		const path = await tempFile();
		await Promise.all([
			addRemote(path, { label: "a", url: "http://192.0.2.1:1", password: "x" }),
			addRemote(path, { label: "b", url: "http://192.0.2.2:2", password: "y" }),
		]);
		expect((await readRemotes(path)).map(({ label }) => label)).toEqual(["a", "b"]);
	});

	it("appends without dropping existing entries", async () => {
		const path = await tempFile('{"remotes":[{"label":"a","url":"http://192.0.2.1:1","password":"x"}]}');
		await addRemote(path, { label: "b", url: "http://192.0.2.2:2", password: "y" });
		const labels = JSON.parse(await readFile(path, "utf8")).remotes.map((r: { label: string }) => r.label);
		expect(labels).toEqual(["a", "b"]);
	});

	it("replaces an entry with the same url rather than duplicating it", async () => {
		const path = await tempFile('{"remotes":[{"label":"old","url":"http://192.0.2.1:1","password":"x"}]}');
		await addRemote(path, { label: "new", url: "http://192.0.2.1:1", password: "z" });
		const remotes = JSON.parse(await readFile(path, "utf8")).remotes;
		expect(remotes).toEqual([{ label: "new", url: "http://192.0.2.1:1", password: "z" }]);
	});
});

const TWO_HOSTS =
	'{"remotes":[{"label":"workbox","url":"http://192.0.2.1:1","password":"old"},{"label":"mini","url":"http://192.0.2.9:9","password":"m"}]}';

async function savedRemotes(path: string): Promise<unknown[]> {
	return JSON.parse(await readFile(path, "utf8")).remotes;
}

describe("updateRemote", () => {
	// The reason this exists: re-enabling the LAN bridge rotates the connection
	// password, and until now the saved entry just died.
	it("changes the password and nothing else", async () => {
		const path = await tempFile(TWO_HOSTS);
		await expect(updateRemote(path, "http://192.0.2.1:1", { password: "rotated" })).resolves.toEqual({
			label: "workbox",
			url: "http://192.0.2.1:1",
			password: "rotated",
		});
		expect(await savedRemotes(path)).toEqual([
			{ label: "workbox", url: "http://192.0.2.1:1", password: "rotated" },
			{ label: "mini", url: "http://192.0.2.9:9", password: "m" },
		]);
	});

	it("renames without touching the saved password", async () => {
		const path = await tempFile(TWO_HOSTS);
		await updateRemote(path, "http://192.0.2.1:1", { label: "the workbox" });
		expect(await savedRemotes(path)).toContainEqual({ label: "the workbox", url: "http://192.0.2.1:1", password: "old" });
	});

	// An explicitly-undefined field survives Electron's structured clone, so
	// "leave the password alone" arrives as a present key with no value.
	it("treats an undefined field as absent rather than as a wipe", async () => {
		const path = await tempFile(TWO_HOSTS);
		await updateRemote(path, "http://192.0.2.1:1", { label: "renamed", password: undefined });
		expect(await savedRemotes(path)).toContainEqual({ label: "renamed", url: "http://192.0.2.1:1", password: "old" });
	});

	it("moves the entry when the url changes instead of cloning it", async () => {
		const path = await tempFile(TWO_HOSTS);
		await updateRemote(path, "http://192.0.2.1:1", { url: "http://192.0.2.5:5" });
		expect(await savedRemotes(path)).toEqual([
			{ label: "workbox", url: "http://192.0.2.5:5", password: "old" },
			{ label: "mini", url: "http://192.0.2.9:9", password: "m" },
		]);
	});

	// Typing the address of a host you already saved must leave one host, not two
	// rows racing to answer for the same machine.
	it("absorbs another entry that already sits on the new url", async () => {
		const path = await tempFile(TWO_HOSTS);
		await updateRemote(path, "http://192.0.2.1:1", { url: "http://192.0.2.9:9" });
		expect(await savedRemotes(path)).toEqual([{ label: "workbox", url: "http://192.0.2.9:9", password: "old" }]);
	});

	it("refuses a url it has never saved", async () => {
		const path = await tempFile(TWO_HOSTS);
		await expect(updateRemote(path, "http://192.0.2.7:7", { label: "ghost" })).rejects.toThrow(/no saved host/);
	});

	it("keeps the file 0600", async () => {
		const path = await tempFile(TWO_HOSTS);
		await updateRemote(path, "http://192.0.2.1:1", { password: "rotated" });
		expect((await stat(path)).mode & 0o777).toBe(0o600);
	});

	it("refuses to touch a file others can read", async () => {
		const path = await tempFile(TWO_HOSTS, 0o644);
		await expect(updateRemote(path, "http://192.0.2.1:1", { password: "rotated" })).rejects.toBeInstanceOf(
			RemotesFilePermissionError,
		);
		// And left the passwords exactly where they were.
		expect(await readFile(path, "utf8")).toBe(TWO_HOSTS);
	});
});

describe("removeRemote", () => {
	it("drops only the named host", async () => {
		const path = await tempFile(TWO_HOSTS);
		await removeRemote(path, "http://192.0.2.1:1");
		expect(await savedRemotes(path)).toEqual([{ label: "mini", url: "http://192.0.2.9:9", password: "m" }]);
	});

	it("keeps the file 0600", async () => {
		const path = await tempFile(TWO_HOSTS);
		await removeRemote(path, "http://192.0.2.1:1");
		expect((await stat(path)).mode & 0o777).toBe(0o600);
	});

	it("is a no-op for a host that was never saved", async () => {
		const path = await tempFile(TWO_HOSTS);
		await removeRemote(path, "http://192.0.2.7:7");
		expect(await readFile(path, "utf8")).toBe(TWO_HOSTS);
	});

	it("refuses to touch a file others can read", async () => {
		const path = await tempFile(TWO_HOSTS, 0o644);
		await expect(removeRemote(path, "http://192.0.2.1:1")).rejects.toBeInstanceOf(RemotesFilePermissionError);
		expect(await readFile(path, "utf8")).toBe(TWO_HOSTS);
	});
});
