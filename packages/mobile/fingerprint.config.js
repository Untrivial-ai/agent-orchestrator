// google-services.json is gitignored and differs between a contributor's machine
// (placeholder), CI (absent) and EAS Build (real). Fingerprint hashes its contents by
// default, which would give Android a runtime version no `eas update` could target.
// It doesn't affect the native ABI, so leave it out.
//
// masked-view's own android/build.gradle rewrites its manifest in place at gradle
// configuration time (it adds or strips `package=` to suit the AGP version), so the
// same checkout hashes differently before and after an Android build — on both
// platforms. The file carries nothing else, so leave it out too.
//
// eas.json is a fingerprint input (reason: easBuild), but the machine that publishes
// updates keeps local-only submit credentials in it (App Store Connect key path and
// ids), held out of git with `git update-index --skip-worktree`. EAS Build sees the
// committed file and the publishing machine sees the local one, so the two hash
// differently and no update would ever match a build. Nothing in it affects the
// native ABI — `channel` is compiled into the binary but controls routing, and
// changing it needs a rebuild regardless — so leave it out.
//
// .gitignore is a fingerprint input too (reason: bareGitIgnore), and the temp-commit
// dance that gets google-services.json past EAS's VCS client deletes a line from it.
// That made every build carry a runtime no normal working tree could reproduce
// (84c9dff7 built vs 53ad3ec1 published), so no update would ever have matched.
// Replacing the dance with an EAS file environment variable removes the need for
// this entry; until then, leave it out.
//
// The rule behind all four: never let a fingerprint input differ between machines
// or between the build state and the publish state.
/** @type {import('@expo/fingerprint').Config} */
module.exports = {
	ignorePaths: [
		"google-services.json",
		"node_modules/@react-native-masked-view/masked-view/android/src/main/AndroidManifest.xml",
		"eas.json",
		".gitignore",
	],
};
