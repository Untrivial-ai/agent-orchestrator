// app.json stays the source of truth for the Expo config. This file exists only to
// put google-services.json where app.json already expects it.
//
// The file is gitignored (origin is public), and EAS Build only uploads what git
// tracks — so it has to reach the builder some other way. It arrives as the
// GOOGLE_SERVICES_JSON *file* environment variable:
//
//   eas env:set --name GOOGLE_SERVICES_JSON --type file \
//     --value ./google-services.json --visibility secret \
//     --environment preview --environment production
//
// EAS materialises it at an absolute path that differs per build and hands us that
// path in the env var. Emitting that path as `android.googleServicesFile` would make
// the resolved config — and therefore the fingerprint runtime version — differ
// between EAS Build and the machine that publishes updates, which is the exact class
// of bug the ignorePaths in fingerprint.config.js exist to prevent. So instead of
// emitting the path, copy the file to the fixed location app.json already points at
// and leave the config value alone. Locally the env var is unset (secret variables
// are not readable outside EAS) and the file is simply already there.
//
// This replaces the temp-commit dance that used to force the file past EAS's VCS
// client. That dance deleted a line from .gitignore, which is itself a fingerprint
// input, so every build carried a runtime no normal working tree could reproduce.
const fs = require("fs");
const path = require("path");

const target = path.join(__dirname, "google-services.json");
const provided = process.env.GOOGLE_SERVICES_JSON;

if (provided && path.resolve(provided) !== target) {
	fs.copyFileSync(provided, target);
}

module.exports = ({ config }) => config;
