import { execFileSync } from "node:child_process";
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, renameSync, rmSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export function buildHostBundle({ source = frontendRoot, output = join(source, "dist-host"), platform = process.platform, arch = process.arch } = {}) {
	if (platform !== "darwin" && platform !== "linux") throw new Error(`Unsupported headless host platform: ${platform}`);
	const daemon = join(source, "daemon", "ao");
	const acpRuntime = join(source, "resources", "acp-runtime");
	const node = join(acpRuntime, "node", "bin/node");
	const adapter = join(acpRuntime, "node_modules", "@agentclientprotocol", "claude-agent-acp", "dist", "index.js");
	const tmux = join(source, "tmux");
	for (const file of [daemon, node, adapter, join(tmux, "bin", "tmux")]) {
		if (!existsSync(file) || !statSync(file).isFile()) throw new Error(`Headless bundle requires ${file}`);
	}

	mkdirSync(output, { recursive: true });
	const staging = mkdtempSync(join(output, ".ao-host-"));
	const archive = join(output, `ao-host-${platform}-${arch}.tar.gz`);
	try {
		const resources = join(staging, "resources");
		mkdirSync(join(resources, "daemon"), { recursive: true });
		cpSync(daemon, join(resources, "daemon", "ao"));
		cpSync(acpRuntime, join(resources, "acp-runtime"), { recursive: true, verbatimSymlinks: true });
		cpSync(tmux, join(resources, "tmux"), { recursive: true });
		for (const file of ["daemon/ao", "acp-runtime/node/bin/node", "tmux/bin/tmux"]) chmodSync(join(resources, file), 0o755);
		const temporary = join(staging, "host.tar.gz");
		execFileSync("tar", ["-czf", temporary, "-C", staging, "resources"], { stdio: "inherit", env: { ...process.env, COPYFILE_DISABLE: "1" } });
		renameSync(temporary, archive);
		return archive;
	} finally {
		rmSync(staging, { recursive: true, force: true });
	}
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
	console.log(buildHostBundle());
}
