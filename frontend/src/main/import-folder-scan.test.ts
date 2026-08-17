// @vitest-environment node
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { scanImportFolder } from "./import-folder-scan";

const execFileAsync = promisify(execFile);

const tempRoots: string[] = [];

async function tempDir(): Promise<string> {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-import-scan-"));
	tempRoots.push(dir);
	return dir;
}

async function git(args: string[], cwd?: string): Promise<string> {
	const { stdout } = await execFileAsync("git", args, {
		cwd,
		env: {
			...process.env,
			GIT_AUTHOR_NAME: "AO Test",
			GIT_AUTHOR_EMAIL: "ao@example.com",
			GIT_COMMITTER_NAME: "AO Test",
			GIT_COMMITTER_EMAIL: "ao@example.com",
		},
	});
	return String(stdout).trim();
}

async function committedRepo(dir: string): Promise<void> {
	await git(["init", "-b", "main", dir]);
	await writeFile(path.join(dir, "README.md"), "hello\n");
	await git(["add", "README.md"], dir);
	await git(["commit", "-m", "initial"], dir);
	await git(["remote", "add", "origin", "https://example.com/repo.git"], dir);
}

beforeEach(() => {
	tempRoots.length = 0;
});

afterEach(async () => {
	await Promise.all(tempRoots.map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("scanImportFolder", () => {
	it("leaves a plain project folder nested inside a parent repo setup-ready with a warning", async () => {
		const root = await tempDir();
		const parent = path.join(root, "parent");
		await committedRepo(parent);
		const nested = path.join(parent, "universe");
		await mkdir(nested);

		const scan = await scanImportFolder(nested, "project");

		expect(scan.path).toBe(nested);
		expect(scan.repos).toEqual([
			expect.objectContaining({
				name: "universe",
				path: nested,
				relativePath: ".",
				status: "ok",
				needsGitInit: true,
			}),
		]);
		expect(scan.setupWarning).toContain("Selected folder is inside an existing Git repository at ");
		expect(scan.setupWarning).toContain("AO will initialize this folder as a separate repository.");
	});

	it("reports a true project repository root as importable", async () => {
		const root = await tempDir();
		const repo = path.join(root, "repo");
		await committedRepo(repo);

		const scan = await scanImportFolder(repo, "project");

		expect(scan.repos).toEqual([
			expect.objectContaining({
				name: "repo",
				path: repo,
				relativePath: ".",
				branch: "auto",
				hasRemote: true,
				status: "ok",
			}),
		]);
	});

	it("leaves a plain non-nested project folder setup-ready", async () => {
		const root = await tempDir();
		const selected = path.join(root, "plain");
		await mkdir(selected);

		const scan = await scanImportFolder(selected, "project", {
			env: { ...process.env, GIT_CEILING_DIRECTORIES: root },
		});

		expect(scan).toEqual({
			path: selected,
			repos: [
				expect.objectContaining({
					name: "plain",
					path: selected,
					relativePath: ".",
					status: "ok",
					needsGitInit: true,
				}),
			],
		});
	});

	it("warns when workspace parent is nested inside an ancestor repo", async () => {
		const root = await tempDir();
		const ancestor = path.join(root, "ancestor");
		await committedRepo(ancestor);
		const parent = path.join(ancestor, "inner");
		await mkdir(parent);
		await committedRepo(path.join(parent, "api"));
		await committedRepo(path.join(parent, "ui"));

		const scan = await scanImportFolder(parent, "workspace");

		expect(scan.setupWarning).toContain("Selected folder is inside an existing Git repository at ");
		expect(scan.repos).toHaveLength(2);
	});

	it("does not warn when workspace parent is a git repo root", async () => {
		const root = await tempDir();
		const parent = path.join(root, "workspace");
		await committedRepo(parent);
		await committedRepo(path.join(parent, "api"));
		await committedRepo(path.join(parent, "ui"));

		const scan = await scanImportFolder(parent, "workspace");

		expect(scan.setupWarning).toBeUndefined();
		expect(scan.repos).toHaveLength(2);
	});

	it("does not warn when workspace parent has no ancestor repo", async () => {
		const root = await tempDir();
		const parent = path.join(root, "workspace");
		await mkdir(parent);
		await committedRepo(path.join(parent, "api"));
		await committedRepo(path.join(parent, "ui"));

		const scan = await scanImportFolder(parent, "workspace", {
			env: { ...process.env, GIT_CEILING_DIRECTORIES: root },
		});

		expect(scan.setupWarning).toBeUndefined();
		expect(scan.repos).toHaveLength(2);
	});

	it("surfaces non-git child folders as needsGitInit in workspace mode", async () => {
		const root = await tempDir();
		const parent = path.join(root, "workspace");
		await mkdir(parent);
		await committedRepo(path.join(parent, "api"));
		await mkdir(path.join(parent, "docs"));

		const scan = await scanImportFolder(parent, "workspace", {
			env: { ...process.env, GIT_CEILING_DIRECTORIES: root },
		});

		expect(scan.setupWarning).toBeUndefined();
		expect(scan.repos).toHaveLength(2);
		const docs = scan.repos.find((r) => r.name === "docs");
		expect(docs).toEqual(
			expect.objectContaining({
				name: "docs",
				status: "ok",
				needsGitInit: true,
				hasRemote: false,
			}),
		);
	});

	it("reports folders inside AO-managed worktrees before offering setup", async () => {
		const home = await tempDir();
		const selected = path.join(home, ".ao", "data", "worktrees", "project", "session");
		await mkdir(selected, { recursive: true });

		const scan = await scanImportFolder(selected, "project", { homeDir: home });

		expect(scan.repos).toEqual([
			expect.objectContaining({
				path: selected,
				relativePath: ".",
				status: "error",
				reason: "Selected folder is inside AO's internal data directory. Select a project folder outside ~/.ao.",
			}),
		]);
	});
});
