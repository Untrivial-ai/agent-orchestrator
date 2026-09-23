// Babel config for the mobile app.
//
// This file did not exist before Reanimated: `babel-preset-expo` is the default
// when no config is present, so nothing needed one. Reanimated 4 does — its
// worklets are produced by a Babel plugin, and without it every animation
// silently runs on the JS thread or throws at runtime.
//
// The worklets plugin MUST be last. It rewrites function bodies into worklets,
// so any plugin that transforms those bodies has to have run already; a plugin
// added after it will see code it does not expect. babel-config.source.test.ts
// pins that ordering, because CI runs no build step and would not otherwise
// catch a config that only fails on a device.
module.exports = function (api) {
	api.cache(true);
	return {
		presets: ["babel-preset-expo"],
		plugins: ["react-native-worklets/plugin"],
	};
};
