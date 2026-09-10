import { createHash } from "node:crypto";
import { chmod, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const VERSION = "0.33.1";
const RELEASE_BASE = `https://github.com/vercel-labs/agent-browser/releases/download/v${VERSION}`;
const OUTPUT_DIR = path.resolve("agent-browser");
const quiet = process.argv.includes("--quiet");
const DOWNLOAD_ATTEMPTS = 3;
const DOWNLOAD_RETRY_DELAY_MS = 1000;

const TARGETS = {
	"darwin-arm64": {
		asset: "agent-browser-darwin-arm64",
		sha256: "33ce6a3f94322ad8ea4ac28db923737c040db88af8bb199f57778995d451f2c7",
	},
	"darwin-x64": {
		asset: "agent-browser-darwin-x64",
		sha256: "e1196791c202e11875dbcd97de744b088f5a94f3706b3f3b4fa9056a5a2d562b",
	},
	"linux-arm64": {
		asset: "agent-browser-linux-arm64",
		sha256: "281cce8e3e9eb11fd823b13c085996d7361c35923ad454ce5cb06a5515630e9b",
	},
	"linux-x64": {
		asset: "agent-browser-linux-x64",
		sha256: "6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa",
	},
	"win32-x64": {
		asset: "agent-browser-win32-x64.exe",
		sha256: "d5520659190d8112833c36ebffa766453a431e6721cce1b03361058080fb38be",
	},
};

const target = TARGETS[`${process.platform}-${process.arch}`];
if (!target) {
	throw new Error(`agent-browser ${VERSION} is not packaged for ${process.platform}-${process.arch}`);
}

const binaryName = process.platform === "win32" ? "agent-browser.exe" : "agent-browser";
const binaryPath = path.join(OUTPUT_DIR, binaryName);
const licenseVersionPath = path.join(OUTPUT_DIR, ".license-version");

await mkdir(OUTPUT_DIR, { recursive: true });
if ((await fileSHA256(binaryPath)) !== target.sha256) {
	const temporaryPath = `${binaryPath}.download`;
	await rm(temporaryPath, { force: true });
	const response = await fetchWithRetry(`${RELEASE_BASE}/${target.asset}`, {
		description: `agent-browser ${VERSION}`,
	});
	if (!response.body) {
		throw new Error(`download agent-browser ${VERSION}: empty response body`);
	}
	await writeFile(temporaryPath, response.body);
	const actual = await fileSHA256(temporaryPath);
	if (actual !== target.sha256) {
		await rm(temporaryPath, { force: true });
		throw new Error(`agent-browser checksum mismatch: expected ${target.sha256}, received ${actual}`);
	}
	await rename(temporaryPath, binaryPath);
	if (process.platform !== "win32") await chmod(binaryPath, 0o755);
}

if ((await readText(licenseVersionPath)).trim() !== VERSION) {
	await Promise.all([
		downloadText(
			`https://unpkg.com/agent-browser@${VERSION}/LICENSE`,
			path.join(OUTPUT_DIR, "LICENSE-agent-browser"),
		),
		downloadText(
			`https://unpkg.com/agent-browser@${VERSION}/cli/src/native/a11y/LICENSE-axe-core.txt`,
			path.join(OUTPUT_DIR, "LICENSE-axe-core"),
		),
		downloadText(
			`https://unpkg.com/agent-browser@${VERSION}/cli/src/native/a11y/LICENSE-axe-core-THIRD-PARTY.txt`,
			path.join(OUTPUT_DIR, "LICENSE-axe-core-THIRD-PARTY"),
		),
	]);
	await writeFile(licenseVersionPath, `${VERSION}\n`, "utf8");
}

if (!quiet) console.log(`Prepared browser automation runtime for ${process.platform}-${process.arch}`);

async function fileSHA256(file) {
	try {
		const contents = await readFile(file);
		return createHash("sha256").update(contents).digest("hex");
	} catch (error) {
		if (error?.code === "ENOENT") return "";
		throw error;
	}
}

async function readText(file) {
	try {
		return await readFile(file, "utf8");
	} catch (error) {
		if (error?.code === "ENOENT") return "";
		throw error;
	}
}

async function downloadText(url, destination) {
	const response = await fetchWithRetry(url, { description: url });
	await writeFile(destination, await response.text(), "utf8");
}

async function fetchWithRetry(url, { description }) {
	let lastError;
	for (let attempt = 1; attempt <= DOWNLOAD_ATTEMPTS; attempt += 1) {
		try {
			const response = await fetch(url, { redirect: "follow" });
			if (response.ok) return response;
			lastError = new Error(`HTTP ${response.status}`);
		} catch (error) {
			lastError = error;
		}

		if (attempt < DOWNLOAD_ATTEMPTS) {
			if (!quiet) console.warn(`Download ${description} failed; retrying (${attempt}/${DOWNLOAD_ATTEMPTS})`);
			await delay(DOWNLOAD_RETRY_DELAY_MS * attempt);
		}
	}
	throw new Error(`download ${description}: ${lastError?.message ?? String(lastError)}`);
}

function delay(ms) {
	return new Promise((resolve) => setTimeout(resolve, ms));
}
