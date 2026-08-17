import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)));
const bannedImportPatterns = [
	/^@tanstack\//,
	/(^|\/)frontend(\/|$)/,
	/(^|\/)(api|api-client|generated|schema)(\/|$|\.)/,
	/(^|\/)(electron|bridge)(\/|$)/,
	/(^|\/)hooks?(\/|$)/,
	/(^|\/)(i18n|stores?|telemetry)(\/|$)/,
];
const allowedExternalImports = new Set(["clsx", "motion/react", "react", "tailwind-merge"]);

describe("product-ui import boundary", () => {
	it("keeps production sources inside the portable package boundary", () => {
		const violations: string[] = [];
		for (const file of productionSourceFiles(sourceRoot)) {
			const source = readFileSync(file, "utf8");
			if (/<a(?:\s|>)/.test(source)) {
				violations.push(`${relative(sourceRoot, file)} owns a direct anchor instead of using the external-link boundary`);
			}
			for (const specifier of importSpecifiers(source)) {
				if (!specifier.startsWith(".") && !allowedExternalImports.has(specifier)) {
					violations.push(`${relative(sourceRoot, file)} imports undeclared boundary module ${specifier}`);
				}
				if (bannedImportPatterns.some((pattern) => pattern.test(specifier))) {
					violations.push(`${relative(sourceRoot, file)} imports banned module ${specifier}`);
				}
				if (specifier.startsWith(".")) {
					const importedFile = resolveImport(file, specifier);
					if (!importedFile || relative(sourceRoot, importedFile).startsWith("..")) {
						violations.push(`${relative(sourceRoot, file)} escapes package source via ${specifier}`);
					}
				}
			}
		}

		expect(violations).toEqual([]);
	});
});

function productionSourceFiles(directory: string): string[] {
	return readdirSync(directory).flatMap((entry) => {
		const path = resolve(directory, entry);
		if (statSync(path).isDirectory()) {
			return entry === "test" ? [] : productionSourceFiles(path);
		}
		return /\.(ts|tsx)$/.test(entry) && !entry.endsWith(".test.ts") && !entry.endsWith(".test.tsx")
			? [path]
			: [];
	});
}

function importSpecifiers(source: string): string[] {
	const specifiers: string[] = [];
	const pattern = /(?:import|export)\s+(?:type\s+)?(?:[\s\S]*?\s+from\s+)?["']([^"']+)["']/g;
	for (const match of source.matchAll(pattern)) {
		if (match[1]) {
			specifiers.push(match[1]);
		}
	}
	return specifiers;
}

function resolveImport(importer: string, specifier: string): string | undefined {
	const base = resolve(dirname(importer), specifier);
	const candidates = extname(base)
		? [base]
		: [base, `${base}.ts`, `${base}.tsx`, resolve(base, "index.ts"), resolve(base, "index.tsx")];
	return candidates.find(existsSync);
}
