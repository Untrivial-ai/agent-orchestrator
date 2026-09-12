import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpus, tmpdir } from "node:os";
import {
	chmodSync,
	copyFileSync,
	existsSync,
	mkdirSync,
	readFileSync,
	renameSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const TMUX = {
	name: "tmux",
	version: "3.5a",
	url: "https://github.com/tmux/tmux/releases/download/3.5a/tmux-3.5a.tar.gz",
	sha256: "16216bd0877170dfcc64157085ba9013610b12b082548c7c9542cc0103198951",
	directory: "tmux-3.5a",
	license: "COPYING",
};
const LIBEVENT = {
	name: "libevent",
	version: "2.1.12-stable",
	url: "https://github.com/libevent/libevent/releases/download/release-2.1.12-stable/libevent-2.1.12-stable.tar.gz",
	sha256: "92e6de1be9ec176428fd2367677e61ceffc2ee1cb119035037a27d346b0403bb",
	directory: "libevent-2.1.12-stable",
	license: "LICENSE",
};
const NCURSES = {
	name: "ncurses",
	version: "6.5",
	url: "https://invisible-mirror.net/archives/ncurses/ncurses-6.5.tar.gz",
	sha256: "136d91bc269a9a5785e5f9e980bc76ab57428f604ce3e5a5a90cebc767971cc6",
	directory: "ncurses-6.5",
	license: "COPYING",
};
const UTF8PROC = {
	name: "utf8proc",
	version: "2.10.0",
	url: "https://github.com/JuliaStrings/utf8proc/archive/refs/tags/v2.10.0.tar.gz",
	sha256: "6f4f1b639daa6dca9f80bc5db1233e9cbaa31a67790887106160b33ef743f136",
	directory: "utf8proc-2.10.0",
	license: "LICENSE.md",
};
const SOURCES = [TMUX, LIBEVENT, NCURSES, UTF8PROC];
const BUILD_REVISION = 4;

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(scriptsDir, "..");
const repoRoot = resolve(frontendRoot, "..");
const outputRoot = join(frontendRoot, "tmux");
const outputBinary = join(outputRoot, "bin", "tmux");

if (process.platform !== "darwin" && process.platform !== "linux") {
	rmSync(outputRoot, { recursive: true, force: true });
	console.log("Bundled tmux is only built for macOS and Linux.");
	process.exit(0);
}

const cacheKey = `${process.platform}-${process.arch}-r${BUILD_REVISION}-${SOURCES.map((source) => source.version).join("-")}`;
// Keep source/build caches outside frontend/: Electron Packager treats that
// directory as the app root and would otherwise copy hundreds of megabytes of
// intermediate objects into app.asar in addition to the tiny extraResource.
const cacheRoot = join(repoRoot, ".cache", "bundled-tmux", cacheKey);
const downloadsRoot = join(repoRoot, ".cache", "bundled-tmux", "downloads");
const sourceRoot = join(cacheRoot, "src");
const prefix = join(cacheRoot, "prefix");
const cachedBinary = join(cacheRoot, "tmux");
const stampPath = join(cacheRoot, "complete.json");
const expectedStamp =
	JSON.stringify({ buildRevision: BUILD_REVISION, platform: process.platform, arch: process.arch, sources: SOURCES }, null, 2) +
	"\n";

mkdirSync(downloadsRoot, { recursive: true });

if (!existsSync(cachedBinary) || !existsSync(stampPath) || readFileSync(stampPath, "utf8") !== expectedStamp) {
	await build();
}

rmSync(outputRoot, { recursive: true, force: true });
mkdirSync(join(outputRoot, "bin"), { recursive: true });
mkdirSync(join(outputRoot, "licenses"), { recursive: true });
copyFileSync(cachedBinary, outputBinary);
chmodSync(outputBinary, 0o755);
for (const source of SOURCES) {
	copyFileSync(join(sourceRoot, source.directory, source.license), join(outputRoot, "licenses", `${source.name}.txt`));
}
writeFileSync(
	join(outputRoot, "VERSIONS.json"),
	JSON.stringify(
		{
			tmux: TMUX.version,
			libevent: LIBEVENT.version,
			ncurses: NCURSES.version,
			utf8proc: UTF8PROC.version,
			platform: process.platform,
			arch: process.arch,
		},
		null,
		2,
	) + "\n",
);

const version = run(outputBinary, ["-V"], { capture: true }).stdout.trim();
if (version !== `tmux ${TMUX.version}`) {
	fail(`bundled tmux reported ${JSON.stringify(version)}, expected ${JSON.stringify(`tmux ${TMUX.version}`)}`);
}
verifyTerminfoAttach(outputBinary);
console.log(`Bundled ${version} at ${outputBinary}`);

async function build() {
	for (const source of SOURCES) await download(source);

	rmSync(sourceRoot, { recursive: true, force: true });
	rmSync(prefix, { recursive: true, force: true });
	mkdirSync(sourceRoot, { recursive: true });
	mkdirSync(prefix, { recursive: true });
	for (const source of SOURCES) {
		run("tar", ["-xzf", archivePath(source), "-C", sourceRoot]);
	}

	const commonEnv = {
		...process.env,
		...(process.platform === "darwin"
			? { MACOSX_DEPLOYMENT_TARGET: process.arch === "arm64" ? "11.0" : "10.15" }
			: {}),
		CPPFLAGS: `-I${join(prefix, "include")} -I${join(prefix, "include", "ncursesw")}`,
		LDFLAGS: `-L${join(prefix, "lib")}`,
		PKG_CONFIG_PATH: [join(prefix, "lib", "pkgconfig"), join(prefix, "lib64", "pkgconfig")].join(":"),
	};
	const jobs = String(Math.max(2, Math.min(cpus().length, 8)));

	const ncursesDir = join(sourceRoot, NCURSES.directory);
	run(
		"./configure",
		[
			`--prefix=${prefix}`,
			"--without-shared",
			"--with-normal",
			"--without-debug",
			"--without-ada",
			"--without-cxx",
			"--without-cxx-binding",
			"--without-tests",
			"--without-manpages",
			"--enable-widec",
			"--with-termlib",
			"--enable-pc-files",
			`--with-pkg-config-libdir=${join(prefix, "lib", "pkgconfig")}`,
		],
		{ cwd: ncursesDir, env: commonEnv },
	);
	run("make", ["-j", jobs], { cwd: ncursesDir, env: commonEnv });
	// Runtime.Attach always presents xterm-256color. Generate its fallback
	// with the matching pinned tic/infocmp we just built: macOS's older host
	// tools cannot compile the ncurses 6.5 entry. Rebuilding embeds the entry
	// into libtinfo so the shipped binary needs no host terminfo database.
	const ncursesBuildDir = join(ncursesDir, "ncurses");
	const fallback = run(
		"sh",
		[
			"-e",
			"./tinfo/MKfallback.sh",
			join(prefix, "share", "terminfo"),
			"../misc/terminfo.src",
			"../progs/tic",
			"../progs/infocmp",
			"xterm-256color",
		],
		{ cwd: ncursesBuildDir, env: commonEnv },
	);
	writeFileSync(join(ncursesBuildDir, "fallback.c"), fallback.stdout);
	run("make", ["-j", jobs], { cwd: ncursesDir, env: commonEnv });
	run("make", ["install.libs", "install.includes"], { cwd: ncursesDir, env: commonEnv });

	const libeventDir = join(sourceRoot, LIBEVENT.directory);
	run(
		"./configure",
		[
			`--prefix=${prefix}`,
			"--disable-shared",
			"--enable-static",
			"--disable-openssl",
			"--disable-samples",
			"--disable-tests",
			"--disable-regress",
			"--disable-benchmarks",
		],
		{ cwd: libeventDir, env: commonEnv },
	);
	run("make", ["-j", jobs], { cwd: libeventDir, env: commonEnv });
	run("make", ["install"], { cwd: libeventDir, env: commonEnv });

	const utf8procDir = join(sourceRoot, UTF8PROC.directory);
	run("make", ["libutf8proc.a", "libutf8proc.pc", `prefix=${prefix}`], { cwd: utf8procDir, env: commonEnv });
	copyFileSync(join(utf8procDir, "libutf8proc.a"), join(prefix, "lib", "libutf8proc.a"));
	copyFileSync(join(utf8procDir, "utf8proc.h"), join(prefix, "include", "utf8proc.h"));
	copyFileSync(join(utf8procDir, "libutf8proc.pc"), join(prefix, "lib", "pkgconfig", "libutf8proc.pc"));

	const tmuxDir = join(sourceRoot, TMUX.directory);
	const tmuxConfigureArgs = ["--enable-utf8proc"];
	if (process.platform === "linux") tmuxConfigureArgs.push("--enable-static");
	const tmuxEnv = {
		...commonEnv,
		LIBTINFO_CFLAGS: `-I${join(prefix, "include", "ncursesw")} -I${join(prefix, "include")}`,
		LIBTINFO_LIBS: join(prefix, "lib", "libtinfow.a"),
		LIBUTF8PROC_CFLAGS: `-I${join(prefix, "include")}`,
		LIBUTF8PROC_LIBS: join(prefix, "lib", "libutf8proc.a"),
	};
	run("./configure", tmuxConfigureArgs, { cwd: tmuxDir, env: tmuxEnv });
	run("make", ["-j", jobs], { cwd: tmuxDir, env: tmuxEnv });

	const builtBinary = join(tmuxDir, "tmux");
	verifyPortableLinkage(builtBinary, prefix);
	copyFileSync(builtBinary, cachedBinary);
	chmodSync(cachedBinary, 0o755);
	writeFileSync(stampPath, expectedStamp);
}

async function download(source) {
	const archive = archivePath(source);
	if (existsSync(archive) && sha256(archive) === source.sha256) return;
	rmSync(archive, { force: true });
	const part = `${archive}.part-${process.pid}`;
	console.log(`Downloading ${source.name} ${source.version}...`);
	let lastError;
	for (let attempt = 1; attempt <= 3; attempt += 1) {
		rmSync(part, { force: true });
		try {
			const response = await fetch(source.url, { redirect: "follow" });
			if (!response.ok) throw new Error(`HTTP ${response.status}`);
			writeFileSync(part, Buffer.from(await response.arrayBuffer()));
			if (sha256(part) !== source.sha256) throw new Error("sha256 mismatch");
			renameSync(part, archive);
			return;
		} catch (error) {
			lastError = error;
			if (attempt < 3) await new Promise((resolveDelay) => setTimeout(resolveDelay, attempt * 1000));
		}
	}
	rmSync(part, { force: true });
	fail(`failed to download ${source.url}: ${lastError instanceof Error ? lastError.message : String(lastError)}`);
}

function archivePath(source) {
	return join(downloadsRoot, `${source.name}-${source.version}.tar.gz`);
}

function sha256(file) {
	return createHash("sha256").update(readFileSync(file)).digest("hex");
}

function verifyPortableLinkage(binary, buildPrefix) {
	if (process.platform === "darwin") {
		const output = run("otool", ["-L", binary], { capture: true }).stdout;
		const external = output
			.split("\n")
			.slice(1)
			.map((line) => line.trim().split(" ")[0])
			.filter(Boolean)
			.filter((library) => !library.startsWith("/usr/lib/") && !library.startsWith("/System/Library/"));
		if (external.length > 0 || output.includes(buildPrefix)) {
			fail(`bundled tmux has non-system dylib dependencies:\n${output}`);
		}
		return;
	}
	const output = run("ldd", [binary], { capture: true, allowFailure: true });
	const linkage = `${output.stdout}\n${output.stderr}`;
	if (/libevent|libncurses|libtinfo|libutf8proc|not found/.test(linkage) || linkage.includes(buildPrefix)) {
		fail(`bundled tmux has unbundled runtime dependencies:\n${linkage}`);
	}
}

function verifyTerminfoAttach(binary) {
	const identity = `ao-tmux-smoke-${process.pid}`;
	const socket = join(tmpdir(), `${identity}.sock`);
	const session = "terminfo";
	const missingTerminfo = join(tmpdir(), identity, "missing-terminfo");
	const attachEnvironment = {
		...process.env,
		HOME: join(tmpdir(), identity, "missing-home"),
		TERM: "xterm-256color",
		TERMINFO: missingTerminfo,
		TERMINFO_DIRS: missingTerminfo,
	};
	run(binary, ["-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", session, "sleep 10"]);
	try {
		const tmuxArgs = [binary, "-S", socket, "-u", "-T", "RGB", "attach-session", "-t", session];
		const result =
			process.platform === "darwin"
				? run("script", ["-q", "/dev/null", ...tmuxArgs], {
						capture: true,
						allowFailure: true,
						ignoreStdin: true,
						env: attachEnvironment,
					})
				: run("script", ["-q", "-e", "-c", tmuxArgs.map(shellQuote).join(" "), "/dev/null"], {
						capture: true,
						allowFailure: true,
						ignoreStdin: true,
						env: attachEnvironment,
					});
		const output = `${result.stdout}\n${result.stderr}`;
		if (result.status !== 0 || /terminfo database/i.test(output)) {
			fail(`bundled tmux could not attach with TERM=xterm-256color:\n${output}`);
		}
	} finally {
		run(binary, ["-S", socket, "kill-server"], { capture: true, allowFailure: true });
		rmSync(socket, { force: true });
	}
}

function shellQuote(value) {
	return `'${value.replaceAll("'", `'\\''`)}'`;
}

function run(command, args, options = {}) {
	const capture = options.capture || process.env.AO_VERBOSE_NATIVE_BUILD !== "1";
	const result = spawnSync(command, args, {
		cwd: options.cwd,
		env: options.env,
		encoding: capture ? "utf8" : undefined,
		maxBuffer: 64 * 1024 * 1024,
		stdio: capture ? [options.ignoreStdin ? "ignore" : "pipe", "pipe", "pipe"] : "inherit",
		windowsHide: true,
	});
	if (result.error) fail(`failed to start ${command}: ${result.error.message}`);
	if (!options.allowFailure && result.status !== 0) {
		if (capture) {
			if (result.stdout) process.stderr.write(result.stdout);
			if (result.stderr) process.stderr.write(result.stderr);
		}
		fail(`${command} exited with status ${result.status}`);
	}
	return { status: result.status, stdout: result.stdout ?? "", stderr: result.stderr ?? "" };
}

function fail(message) {
	console.error(message);
	process.exit(1);
}
