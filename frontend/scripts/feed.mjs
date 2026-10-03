// Generates electron-updater feed metadata (latest*.yml / nightly*.yml) plus
// gzip sidecar blockmaps for a release's versioned installers. Dependency-free
// ESM (mirrors nightly-version.mjs) so CI runs `node scripts/feed.mjs` directly
// and vitest unit-tests the pure functions. The only non-stdlib reach is the
// blockmap wrapper (Task 1).
// Pass --important to emit `important: true` in each generated yml. An
// already-published nightly can be retro-flagged by re-running the feed job
// with --important set (or editing the yml and running
// `gh release upload TAG nightly*.yml --clobber`).
import { readdirSync, writeFileSync, readFileSync, statSync, openSync, readSync, closeSync } from "node:fs";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { inflateRawSync } from "node:zlib";
import { writeBlockmap } from "./blockmap.mjs";

// selectInstallers picks the versioned, auto-updatable installers from a release
// download dir, grouped by platform/arch. Excludes the ao-start aliases (no
// version string in their names) and deb/rpm (system-package-managed). The mac
// arch split keys on the literal "arm64" substring, the same discriminator the
// updater (MacUpdater.filterFilesForArch) uses.
export function selectInstallers(filenames, version) {
	const versioned = filenames.filter((f) => f.includes(version));
	const isDarwinZip = (f) => f.endsWith(".zip") && f.includes("darwin");
	return {
		win: versioned.filter((f) => f.endsWith(".exe")),
		linux: versioned.filter((f) => f.endsWith(".AppImage")),
		macArm64: versioned.filter((f) => isDarwinZip(f) && f.includes("arm64")),
		macX64: versioned.filter((f) => isDarwinZip(f) && !f.includes("arm64")),
	};
}

// feedFilename maps (channel, platform) to electron-updater's expected feed name.
// The updater adds its own OS/arch suffix client-side; we name the published
// asset to match: "" (win), "-mac", "-linux" (x64 Linux).
export function feedFilename(channel, platform) {
	const suffix = platform === "mac" ? "-mac" : platform === "linux" ? "-linux" : "";
	return `${channel}${suffix}.yml`;
}

// buildYml serializes one platform's feed. files is [{ url, sha512, size }];
// for mac the arm64 entry comes first. The deprecated top-level path/sha512
// point at files[0].
//
// blockMapSize is written only for linux, where AppImageUpdater reads the new
// file's blockmap from the AppImage's own embedded tail. Per platform that means
// (verified against the published electron-updater@6.8.9 tarball, see #3288
// workstream 3):
//   win:   NsisUpdater takes AppUpdater.differentialDownloadInstaller, which
//          fetches the "<installer>.blockmap" sidecar. Omitting blockMapSize is
//          what selects that sidecar path, so win sidecars are load-bearing.
//   linux: AppImageUpdater NEVER fetches a sidecar. It uses
//          FileWithEmbeddedBlockMapDifferentialDownloader, which reads the
//          blockmap from the AppImage's own tail at
//          `fileSize - (blockMapSize + 4)` and therefore hard-requires
//          blockMapSize in the yml. The maker appends an embedded blockmap
//          (deflate + 4-byte big-endian size footer) to every AppImage at build
//          time (makers/maker-appimage.ts -> app-builder-lib appendBlockmap),
//          so the feed emits the footer value (readEmbeddedBlockMapSize); with
//          it the updater diffs against the running AppImage's map and fetches
//          only the changed chunks. Feed ymls that omit it send the updater a
//          NaN range (ERR_OUT_OF_RANGE) and force a full download (#5576).
//          The linux .blockmap sidecars we publish are consumed by nothing (see
//          the note in generateFeeds).
//   mac:   no sidecar is generated on any channel. Absence is the global
//          barrier protecting older clients (#3034/#3151/#3267).
//
// When important is true, emits `important: true` after releaseDate so the
// in-app update prompt is escalated.
export function buildYml(version, files, releaseDate, important = false) {
	const lines = [`version: ${version}`, "files:"];
	for (const f of files) {
		lines.push(`  - url: ${f.url}`);
		lines.push(`    sha512: ${f.sha512}`);
		lines.push(`    size: ${f.size}`);
		if (f.blockMapSize !== undefined) lines.push(`    blockMapSize: ${f.blockMapSize}`);
	}
	lines.push(`path: ${files[0].url}`);
	lines.push(`sha512: ${files[0].sha512}`);
	lines.push(`releaseDate: '${releaseDate}'`);
	if (important) lines.push("important: true");
	return lines.join("\n") + "\n";
}

// generateFeeds writes the yml + sidecar blockmaps for every platform present in
// dir. version may carry +build metadata (nightly); strip it for the yml.
// mac zips never produce sidecars, including Nightly. The release conductor
// must independently suppress them while delivery isolation and runtime safety
// are unresolved. Local client flags cannot protect older binaries.
//
// The linux sidecars this still writes are dead weight: AppImageUpdater reads
// its blockmap from the AppImage tail, never from a sidecar. Generation is kept
// deliberately rather than dropped, because the release publish guard lives in
// a separate private pipeline that currently asserts on the linux sidecar
// filenames; dropping the files here would red that guard out of band. Removing
// both together is tracked as its own decision on #3288 workstream 3.
export async function generateFeeds(dir, rawVersion, channel, releaseDate, important = false) {
	const version = rawVersion.split("+")[0];
	const sel = selectInstallers(readdirSync(dir), version);
	const groups = [
		{ platform: "win", names: sel.win },
		{ platform: "linux", names: sel.linux },
		{ platform: "mac", names: [...sel.macArm64, ...sel.macX64] }, // arm64 first
	];
	for (const { platform, names } of groups) {
		if (names.length === 0) continue;
		const files = [];
		for (const name of names) {
			const filePath = join(dir, name);
			const { sha512, size } =
				platform === "mac"
					? hashFile(filePath)
					: await writeBlockmap(filePath);
			// linux feeds surface the embedded blockmap size (see the buildYml
			// platform notes); win/mac stay sidecar/full-download shaped.
			const blockMapSize = platform === "linux" ? readEmbeddedBlockMapSize(filePath) : undefined;
			files.push({ url: name, sha512, size, blockMapSize });
		}
		writeFileSync(join(dir, feedFilename(channel, platform)), buildYml(version, files, releaseDate, important));
	}
}

// CLI: node scripts/feed.mjs <dir> <version> <channel> [--important]
if (import.meta.url === `file://${process.argv[1]}`) {
	const [, , dir, version, channel] = process.argv;
	if (!dir || !version || !channel) {
		process.stderr.write("usage: node feed.mjs <dir> <version> <channel>\n");
		process.exit(2);
	}
	const important = process.argv.includes("--important");
	generateFeeds(dir, version, channel, new Date().toISOString(), important).catch((err) => {
		process.stderr.write(`${err.stack || err}\n`);
		process.exit(1);
	});
}

// hashFile computes the same {sha512, size} shape writeBlockmap returns, but
// without writing a .blockmap sidecar file. Used for mac zips on every channel:
// Squirrel.Mac's ShipIt install step runs `ditto` against the extracted update
// cache and fails on a corrupt one, which is the #3034 failure signature.
//
// The original zip-format theory (that ShipIt failed because
// @electron-forge/maker-zip's output lacked the AppleDouble "._*" entries
// electron-builder's ditto-based zips carry) did NOT hold. A production
// nightly-to-stable channel switch reproduced the same corruption against a
// target zip that was already correctly ditto-built with its full AppleDouble
// set. The defect is in the differential download/patch-apply mechanism itself,
// not in the zip format (#3267 decision 4). Skipping the sidecar for mac
// therefore remains the baseline on every channel. #3267 decision 4 records
// the safety boundary for any future rollout.
export function hashFile(filePath) {
	const data = readFileSync(filePath);
	const sha512 = createHash("sha512").update(data).digest("base64");
	const size = statSync(filePath).size;
	return { sha512, size };
}

// readEmbeddedBlockMapSize returns the blockMapSize of an AppImage's embedded
// blockmap tail, or undefined when the tail is absent or inconsistent. The maker
// appends `deflate(blockmap v2) + 4-byte big-endian size` to every AppImage
// (app-builder-lib appendBlockmap), so the size is the file's final 4 bytes.
// Before trusting it, re-inflate the tail and require the chunk sizes to cover
// the file exactly (sum(sizes) + blockMapSize + 4 === file size), so a stale or
// truncated tail can never publish a size that makes the updater read garbage.
// A non-linux file (no tail) or a corrupt tail returns undefined, and buildYml
// then omits the field: the updater falls back to a full download, exactly as
// pre-#5576 clients behave.
export function readEmbeddedBlockMapSize(filePath) {
	const size = statSync(filePath).size;
	if (size < 5) return undefined;
	const fd = openSync(filePath, "r");
	try {
		const footer = Buffer.allocUnsafe(4);
		readSync(fd, footer, 0, 4, size - 4);
		const blockMapSize = footer.readUInt32BE(0);
		const start = size - 4 - blockMapSize;
		if (blockMapSize <= 0 || start < 0) return undefined;
		const compressed = Buffer.allocUnsafe(blockMapSize);
		readSync(fd, compressed, 0, blockMapSize, start);
		const blockMap = JSON.parse(inflateRawSync(compressed).toString());
		const fileEntry = blockMap?.files?.[0];
		if (blockMap?.version !== "2" || fileEntry == null ||
			!Array.isArray(fileEntry.checksums) || !Array.isArray(fileEntry.sizes) ||
			fileEntry.checksums.length !== fileEntry.sizes.length) return undefined;
		const covered = fileEntry.sizes.reduce((sum, n) => sum + n, 0);
		if (covered + blockMapSize + 4 !== size) return undefined;
		return blockMapSize;
	} catch {
		return undefined;
	} finally {
		closeSync(fd);
	}
}
