import { execFile } from "node:child_process";
import { readdir, stat } from "node:fs/promises";
import { realpathSync } from "node:fs";
import path from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

export type GitRepoScanResult = {
	name: string;
	path: string;
	relativePath: string;
	branch: string;
	remote: string;
	hasRemote: boolean;
	status: "ok" | "error";
	reason?: string;
	needsGitInit?: boolean;
};

export type ImportFolderScanResult = {
	path: string;
	repos: GitRepoScanResult[];
	setupWarning?: string;
};

const IMPORT_SCAN_CONCURRENCY = 8;
const IMPORT_SCAN_MAX_ENTRIES = 200;
const IMPORT_SCAN_SKIP_DIRS = new Set([
	".git",
	"node_modules",
	"dist",
	"build",
	".cache",
	".turbo",
	"target",
	"coverage",
	"tmp",
	"temp",
	"Library",
]);

type ScanOptions = {
	env?: NodeJS.ProcessEnv;
	homeDir?: string;
};

async function gitOutput(cwd: string, args: string[], options: ScanOptions = {}): Promise<string> {
	const { stdout } = await execFileAsync("git", args, { cwd, env: options.env, timeout: 5000 });
	return String(stdout).trim();
}

function comparablePath(value: string): string {
	const resolved = path.resolve(value);
	try {
		const real = realpathSync(resolved);
		return process.platform === "win32" ? real.toLowerCase() : real;
	} catch {
		return process.platform === "win32" ? resolved.toLowerCase() : resolved;
	}
}

function samePath(a: string, b: string): boolean {
	return comparablePath(a) === comparablePath(b);
}

function normalizeGitReportedPath(cwd: string, value: string): string {
	if (!value) return "";
	return path.resolve(cwd, value);
}

function isDescendantPath(child: string, parent: string): boolean {
	const childKey = comparablePath(child);
	const parentKey = comparablePath(parent);
	return childKey === parentKey || childKey.startsWith(parentKey + path.sep);
}

function projectSetupSafetyReason(repoPath: string, options: ScanOptions = {}): string | undefined {
	const home = options.homeDir?.trim();
	if (!home) return undefined;
	const aoState = path.join(home, ".ao");
	if (isDescendantPath(repoPath, aoState)) {
		return "Selected folder is inside AO's internal data directory. Select a project folder outside ~/.ao.";
	}
	return undefined;
}

export async function ancestorRepositorySetupWarning(repoPath: string, options: ScanOptions = {}): Promise<string | undefined> {
	try {
		const top = normalizeGitReportedPath(repoPath, await gitOutput(repoPath, ["rev-parse", "--show-toplevel"], options));
		if (top && !samePath(top, repoPath)) {
			return `Selected folder is inside an existing Git repository at ${top}. AO will initialize this folder as a separate repository.`;
		}
	} catch {
		// No ancestor repository.
	}
	return undefined;
}

async function isGitRepo(repoPath: string, options: ScanOptions = {}): Promise<boolean> {
	try {
		const gitInfo = await stat(path.join(repoPath, ".git"));
		if (!gitInfo.isDirectory()) return false;
		await gitOutput(repoPath, ["rev-parse", "--show-toplevel"], options);
		return true;
	} catch {
		return false;
	}
}

async function resolveDefaultBranch(repoPath: string, options: ScanOptions = {}): Promise<string> {
	try {
		const ref = await gitOutput(repoPath, ["symbolic-ref", "--short", "refs/remotes/origin/HEAD"], options);
		if (ref) return ref.replace(/^origin\//, "");
	} catch {
		// The current checkout may be temporary and is not a repository default.
	}
	return "auto";
}

async function scanGitRepo(
	repoPath: string,
	rootPath: string,
	options: ScanOptions = {},
): Promise<GitRepoScanResult | null> {
	const relativePath = repoPath === rootPath ? "." : path.relative(rootPath, repoPath);
	const name = path.basename(repoPath);
	try {
		const gitInfo = await stat(path.join(repoPath, ".git"));
		if (!gitInfo.isDirectory()) {
			return {
				name,
				path: repoPath,
				relativePath,
				branch: "",
				remote: "",
				hasRemote: false,
				status: "ok",
				needsGitInit: true,
			};
		}
	} catch {
		try {
			if ((await gitOutput(repoPath, ["rev-parse", "--is-bare-repository"], options)) === "true") {
				return {
					name,
					path: repoPath,
					relativePath,
					branch: "HEAD",
					remote: "",
					hasRemote: false,
					status: "error",
					reason: "Bare repositories cannot be imported.",
				};
			}
		} catch {
			// Not a git repository — surface as needs-init.
		}
		return {
			name,
			path: repoPath,
			relativePath,
			branch: "",
			remote: "",
			hasRemote: false,
			status: "ok",
			needsGitInit: true,
		};
	}
	if (!(await isGitRepo(repoPath, options))) return null;
	const [branchResult, remoteResult, headResult] = await Promise.allSettled([
		resolveDefaultBranch(repoPath, options),
		gitOutput(repoPath, ["remote", "get-url", "origin"], options),
		gitOutput(repoPath, ["rev-parse", "--verify", "HEAD"], options),
	]);
	const hasHead = headResult.status === "fulfilled";
	const hasRemote = remoteResult.status === "fulfilled" && remoteResult.value.length > 0;
	const validationReason = scanRepoValidationReason(name);
	return {
		name,
		path: repoPath,
		relativePath,
		branch: branchResult.status === "fulfilled" && branchResult.value ? branchResult.value : "HEAD",
		remote: remoteResult.status === "fulfilled" ? remoteResult.value : "",
		hasRemote,
		status: validationReason ? "error" : "ok",
		reason: validationReason,
		needsGitInit: !validationReason && (!hasHead || !hasRemote),
	};
}

function scanRepoValidationReason(name: string): string | undefined {
	if (name === "__root__") return "Repository name is reserved by AO.";
	return undefined;
}

async function mapLimited<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
	const out = new Array<R>(items.length);
	let next = 0;
	await Promise.all(
		Array.from({ length: Math.min(limit, items.length) }, async () => {
			for (;;) {
				const index = next++;
				if (index >= items.length) return;
				out[index] = await fn(items[index]);
			}
		}),
	);
	return out;
}

export async function scanImportFolder(
	rootPath: string,
	mode: "project" | "workspace",
	options: ScanOptions = {},
): Promise<ImportFolderScanResult> {
	if (mode === "project") {
		const safetyReason = projectSetupSafetyReason(rootPath, options);
		if (safetyReason) {
			return {
				path: rootPath,
				repos: [
					{
						name: path.basename(rootPath),
						path: rootPath,
						relativePath: ".",
						branch: "HEAD",
						remote: "",
						hasRemote: false,
						status: "error",
						reason: safetyReason,
					},
				],
			};
		}
	const repo = await scanGitRepo(rootPath, rootPath, options);
	if (repo && !repo.needsGitInit) return { path: rootPath, repos: [repo] };
	const setupWarning = await ancestorRepositorySetupWarning(rootPath, options);
	return {
		path: rootPath,
		repos: repo ? [repo] : [],
		...(setupWarning ? { setupWarning } : {}),
	};
	}

	const [entries, ancestorWarning] = await Promise.all([
		readdir(rootPath, { withFileTypes: true }),
		ancestorRepositorySetupWarning(rootPath, options),
	]);
	const repos = await mapLimited(
		entries
			.filter((entry) => entry.isDirectory() && !IMPORT_SCAN_SKIP_DIRS.has(entry.name))
			.slice(0, IMPORT_SCAN_MAX_ENTRIES),
		IMPORT_SCAN_CONCURRENCY,
		(entry) => scanGitRepo(path.join(rootPath, entry.name), rootPath, options),
	);
	return {
		path: rootPath,
		repos: repos
			.filter((repo): repo is GitRepoScanResult => repo !== null)
			.sort((a, b) => a.name.localeCompare(b.name)),
		...(ancestorWarning ? { setupWarning: ancestorWarning } : {}),
	};
}
