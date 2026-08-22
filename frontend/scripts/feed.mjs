// Generates electron-updater feed metadata (latest*.yml / nightly*.yml) plus
// gzip sidecar blockmaps for the Windows installer, the only artifact whose
// updater actually fetches one (see generateFeeds). Dependency-free
// ESM (mirrors nightly-version.mjs) so CI runs `node scripts/feed.mjs` directly
// and vitest unit-tests the pure functions. The only non-stdlib reach is the
// blockmap wrapper (Task 1).
// Pass --important to emit `important: true` in each generated yml. An
// already-published nightly can be retro-flagged by re-running the feed job
// with --important set (or editing the yml and running
// `gh release upload TAG nightly*.yml --clobber`).
import { readdirSync, writeFileSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { createHash } from "node:crypto";
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
// blockMapSize is never written. Per platform that means (verified against the
// published electron-updater@6.8.9 tarball, see #3288 workstream 3):
//   win:   NsisUpdater takes AppUpdater.differentialDownloadInstaller, which
//          fetches the "<installer>.blockmap" sidecar. Omitting blockMapSize is
//          what selects that sidecar path, so win sidecars are load-bearing.
//   linux: AppImageUpdater NEVER fetches a sidecar. It uses
//          FileWithEmbeddedBlockMapDifferentialDownloader, which reads the
//          blockmap from the AppImage's own tail at
//          `fileSize - (blockMapSize + 4)` and therefore hard-requires
//          blockMapSize in the yml. With it absent, linux differential updates
//          cannot run at all and every client falls back to a full download.
//          No linux sidecar is generated, since nothing would read it.
//   mac:   no sidecar is generated either (see hashFile), so MacUpdater always
//          takes the full-download path. Settled permanent baseline, #3151/#3267.
//
// When important is true, emits `important: true` after releaseDate so the
// in-app update prompt is escalated.
export function buildYml(version, files, releaseDate, important = false) {
	const lines = [`version: ${version}`, "files:"];
	for (const f of files) {
		lines.push(`  - url: ${f.url}`);
		lines.push(`    sha512: ${f.sha512}`);
		lines.push(`    size: ${f.size}`);
	}
	lines.push(`path: ${files[0].url}`);
	lines.push(`sha512: ${files[0].sha512}`);
	lines.push(`releaseDate: '${releaseDate}'`);
	if (important) lines.push("important: true");
	return lines.join("\n") + "\n";
}

// generateFeeds writes the yml for every platform present in dir, plus a
// .blockmap sidecar for the windows installer only. version may carry +build
// metadata (nightly); strip it for the yml.
//
// Windows is the only platform whose updater fetches a sidecar
// (AppUpdater.differentialDownloadInstaller, reached from NsisUpdater). mac
// skips it by policy so MacUpdater always takes the full download (#3034,
// #3151, #3267 decision 4); linux never had a reader at all, since
// AppImageUpdater takes its blockmap from the AppImage tail and needs a
// blockMapSize buildYml does not emit. Linux generation was retired in #3288
// workstream 3; the private publish guard treats linux sidecars as optional,
// so their absence is not a publish failure.
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
			const { sha512, size } =
				platform === "win" ? await writeBlockmap(join(dir, name)) : hashFile(join(dir, name));
			files.push({ url: name, sha512, size });
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
// without writing a .blockmap sidecar file. Used for every platform except
// windows. For linux the reason is simply that nothing reads the sidecar (see
// generateFeeds). For mac it is a deliberate fix: Squirrel.Mac's ShipIt install
// step runs `ditto` against the extracted update cache and fails on a corrupt
// one, which is the #3034 failure signature.
//
// The original zip-format theory (that ShipIt failed because
// @electron-forge/maker-zip's output lacked the AppleDouble "._*" entries
// electron-builder's ditto-based zips carry) did NOT hold. A production
// nightly-to-stable channel switch reproduced the same corruption against a
// target zip that was already correctly ditto-built with its full AppleDouble
// set. The defect is in the differential download/patch-apply mechanism itself,
// not in the zip format (#3267 decision 4). Skipping the sidecar for mac is
// therefore the actual fix, not a workaround: it forces MacUpdater onto a full
// zip download every time. Permanent baseline, #3151; #3267 decision 4 records
// the evidence bar for ever revisiting it.
export function hashFile(filePath) {
	const data = readFileSync(filePath);
	const sha512 = createHash("sha512").update(data).digest("base64");
	const size = statSync(filePath).size;
	return { sha512, size };
}
