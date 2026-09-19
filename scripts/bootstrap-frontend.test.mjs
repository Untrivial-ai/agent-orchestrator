import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
	REQUIRED_NODE_VERSION,
	REQUIRED_NPM_VERSION,
	assertSupportedRuntime,
	createBootstrapKey,
	canReuseBootstrap,
	commandForPlatform,
} from "./bootstrap-frontend.mjs";

const repositoryFile = (path) => new URL(`../${path}`, import.meta.url);

test("runtime selectors and package contracts stay aligned", () => {
	for (const file of [".node-version", ".nvmrc"]) {
		assert.equal(readFileSync(repositoryFile(file), "utf8").trim(), REQUIRED_NODE_VERSION);
	}
	for (const file of ["package.json", "frontend/package.json"]) {
		const packageJson = JSON.parse(readFileSync(repositoryFile(file), "utf8"));
		assert.equal(packageJson.packageManager, `npm@${REQUIRED_NPM_VERSION}`);
		assert.equal(packageJson.engines.node, "24.20.x");
		assert.equal(packageJson.engines.npm, "11.19.x");
	}
});

test("accepts only the repository-pinned Node and npm versions", () => {
	assert.doesNotThrow(() =>
		assertSupportedRuntime(`v${REQUIRED_NODE_VERSION}`, REQUIRED_NPM_VERSION),
	);
	assert.throws(
		() => assertSupportedRuntime("v18.20.0", REQUIRED_NPM_VERSION),
		/newer or older runtimes can omit required native packages/,
	);
	assert.throws(
		() => assertSupportedRuntime(`v${REQUIRED_NODE_VERSION}`, "10.9.2"),
		/newer or older runtimes can omit required native packages/,
	);
});

test("bootstrap key changes for every dependency compatibility boundary", () => {
	const base = {
		platform: "darwin",
		arch: "arm64",
		nodeVersion: `v${REQUIRED_NODE_VERSION}`,
		npmVersion: REQUIRED_NPM_VERSION,
		lockfiles: [
			{ path: "package-lock.json", content: "root-lock" },
			{ path: "frontend/package-lock.json", content: "frontend-lock" },
		],
	};
	const key = createBootstrapKey(base);
	assert.equal(createBootstrapKey({ ...base, lockfiles: [...base.lockfiles].reverse() }), key);
	for (const changed of [
		{ ...base, platform: "linux" },
		{ ...base, arch: "x64" },
		{ ...base, nodeVersion: "v24.19.0" },
		{ ...base, npmVersion: "11.17.0" },
		{
			...base,
			lockfiles: [
				{ path: "package-lock.json", content: "root-lock" },
				{ path: "frontend/package-lock.json", content: "changed" },
			],
		},
	]) {
		assert.notEqual(createBootstrapKey(changed), key);
	}
});

test("reuse requires both the matching key and a successful validation", () => {
	assert.equal(canReuseBootstrap("same", "same", true), true);
	assert.equal(canReuseBootstrap("same", "same", false), false);
	assert.equal(canReuseBootstrap("old", "new", true), false);
	assert.equal(canReuseBootstrap(undefined, "new", true), false);
});

test("Windows uses the npm shim without rewriting the Node executable", () => {
	assert.equal(commandForPlatform("npm", "win32"), "npm.cmd");
	assert.equal(commandForPlatform("node", "win32"), "node");
	assert.equal(commandForPlatform("npm", "linux"), "npm");
});
