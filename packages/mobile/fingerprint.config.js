// google-services.json is gitignored and differs between a contributor's machine
// (placeholder), CI (absent) and EAS Build (real). Fingerprint hashes its contents by
// default, which would give Android a runtime version no `eas update` could target.
// It doesn't affect the native ABI, so leave it out.
//
// masked-view's own android/build.gradle rewrites its manifest in place at gradle
// configuration time (it adds or strips `package=` to suit the AGP version), so the
// same checkout hashes differently before and after an Android build — on both
// platforms. The file carries nothing else, so leave it out too.
/** @type {import('@expo/fingerprint').Config} */
module.exports = {
	ignorePaths: [
		"google-services.json",
		"node_modules/@react-native-masked-view/masked-view/android/src/main/AndroidManifest.xml",
	],
};
