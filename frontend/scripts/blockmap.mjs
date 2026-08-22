// frontend/scripts/blockmap.mjs
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

// The ONE fragile import. app-builder-lib 26.x generates blockmaps in pure JS
// (there is no app-builder CLI / app-builder-bin in this tree). This internal
// path is pinned via package-lock; if it moves on a major upgrade, only this
// file changes. Smoke-tested by blockmap.test.mjs.
const { buildBlockMap } = require("app-builder-lib/out/targets/blockmap/blockmap.js");

// writeBlockmap creates "<filePath>.blockmap" (gzip sidecar) and returns the
// file's base64 sha512 + byte size, exactly as electron-updater reads them from
// the feed yml. We deliberately do NOT surface blockMapSize.
//
// Only the windows installer is passed here; feed.mjs uses hashFile for every
// other platform. Why, per platform (verified against the published
// electron-updater@6.8.9 tarball, #3288 workstream 3):
//   win:   omitting blockMapSize selects the sidecar differential path in
//          NsisUpdater, via AppUpdater.differentialDownloadInstaller. Sidecars
//          are load-bearing.
//   linux: AppImageUpdater has no sidecar path at all; it uses
//          FileWithEmbeddedBlockMapDifferentialDownloader, reading the blockmap
//          from the AppImage tail and requiring blockMapSize. With that absent,
//          linux differential updates are off entirely and a sidecar would be
//          read by nobody, so none is generated (#3288 workstream 3).
//   mac:   no sidecar either, so MacUpdater always takes the full zip download
//          (#3034, #3151, #3267 decision 4).
export async function writeBlockmap(filePath) {
	const { sha512, size } = await buildBlockMap(filePath, "gzip", `${filePath}.blockmap`);
	return { sha512, size };
}
