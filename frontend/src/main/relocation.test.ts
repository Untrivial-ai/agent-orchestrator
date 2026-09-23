// @vitest-environment node
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
	decideRelocation,
	installedBundlePath,
	readBundleVersion,
	type RelocationInputs,
} from "./relocation";

const BASE: RelocationInputs = {
	inApplicationsFolder: false,
	installedPresent: true,
	installedVersion: "0.11.2-nightly.202608050602",
	runningVersion: "0.10.3",
};

describe("decideRelocation", () => {
	// The reported bug: a stale 0.10.3 download launching while /Applications
	// holds a newer nightly. This MUST NOT relocate, or the nightly is trashed.
	it("hands off when the install is newer", () => {
		expect(decideRelocation(BASE)).toBe("handoff");
	});

	it("hands off when the install is the same version", () => {
		expect(
			decideRelocation({ ...BASE, installedVersion: "0.10.3" }),
		).toBe("handoff");
	});

	it("relocates when /Applications is empty", () => {
		expect(
			decideRelocation({
				...BASE,
				installedPresent: false,
				installedVersion: null,
			}),
		).toBe("relocate");
	});

	it("relocates over a strictly older install", () => {
		expect(
			decideRelocation({
				...BASE,
				installedVersion: "0.10.3",
				runningVersion: "0.12.0",
			}),
		).toBe("relocate");
	});

	it("treats a nightly as older than the stable it precedes", () => {
		expect(
			decideRelocation({
				...BASE,
				installedVersion: "0.11.2-nightly.202608050602",
				runningVersion: "0.11.2",
			}),
		).toBe("relocate");
	});

	it("stays put when already in an Applications folder", () => {
		expect(
			decideRelocation({ ...BASE, inApplicationsFolder: true }),
		).toBe("stay");
	});

	// Never destroy an install we could not identify, and never hand the
	// session to a bundle we could not identify.
	it("stays put when the installed version is unreadable", () => {
		expect(decideRelocation({ ...BASE, installedVersion: null })).toBe("stay");
	});

	it("stays put when either version is not semver", () => {
		expect(
			decideRelocation({ ...BASE, installedVersion: "garbage" }),
		).toBe("stay");
		expect(
			decideRelocation({ ...BASE, runningVersion: "garbage" }),
		).toBe("stay");
	});
});

describe("installedBundlePath", () => {
	it("maps any running bundle to the same name under /Applications", () => {
		expect(
			installedBundlePath("/Users/x/Downloads/Agent Orchestrator.app"),
		).toBe("/Applications/Agent Orchestrator.app");
		expect(
			installedBundlePath("/Volumes/Agent Orchestrator/Agent Orchestrator.app"),
		).toBe("/Applications/Agent Orchestrator.app");
	});
});

describe("readBundleVersion", () => {
	let dir: string;
	let bundle: string;

	beforeEach(async () => {
		dir = await mkdtemp(path.join(os.tmpdir(), "ao-relocation-"));
		bundle = path.join(dir, "Agent Orchestrator.app");
		await mkdir(path.join(bundle, "Contents"), { recursive: true });
	});

	afterEach(async () => {
		await rm(dir, { recursive: true, force: true });
	});

	async function writePlist(body: string): Promise<void> {
		await writeFile(path.join(bundle, "Contents", "Info.plist"), body, "utf8");
	}

	// Shaped like a real Forge-written Info.plist: XML, other keys around it.
	it("reads the short version string out of an XML plist", async () => {
		await writePlist(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>CFBundleDisplayName</key>
    <string>Agent Orchestrator</string>
    <key>CFBundleShortVersionString</key>
    <string>0.11.2-nightly.202608050602</string>
    <key>CFBundleVersion</key>
    <string>0.11.2-nightly.202608050602</string>
  </dict>
</plist>
`);
		expect(readBundleVersion(bundle)).toBe("0.11.2-nightly.202608050602");
	});

	it("returns null when the key is missing", async () => {
		await writePlist("<plist><dict></dict></plist>");
		expect(readBundleVersion(bundle)).toBeNull();
	});

	it("returns null when the bundle does not exist", () => {
		expect(readBundleVersion(path.join(dir, "Nope.app"))).toBeNull();
	});
});
