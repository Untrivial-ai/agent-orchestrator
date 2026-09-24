const fs = require("node:fs");
const path = require("node:path");
const { withDangerousMod } = require("@expo/config-plugins");

const source = path.join(__dirname, "..", "assets", "icons", "CircleDashedCheck.imageset");

function copyCircleDashedCheckAssets(platformProjectRoot, projectName) {
	const target = path.join(platformProjectRoot, projectName, "Images.xcassets", "CircleDashedCheck.imageset");
	fs.mkdirSync(target, { recursive: true });
	for (const filename of fs.readdirSync(source)) {
		fs.copyFileSync(path.join(source, filename), path.join(target, filename));
	}
}

function withCircleDashedCheck(config) {
	return withDangerousMod(config, ["ios", (config) => {
		copyCircleDashedCheckAssets(config.modRequest.platformProjectRoot, config.modRequest.projectName);
		return config;
	}]);
}

module.exports = withCircleDashedCheck;
module.exports.copyCircleDashedCheckAssets = copyCircleDashedCheckAssets;
