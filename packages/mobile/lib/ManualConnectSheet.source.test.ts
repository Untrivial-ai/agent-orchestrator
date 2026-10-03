import { readFileSync } from "node:fs";
import { expect, it } from "vitest";

it("does not send machine A's saved bearer when manually pairing machine B", () => {
	const sheet = readFileSync(new URL("./ManualConnectSheet.tsx", import.meta.url), "utf8");
	const config = readFileSync(new URL("./config.ts", import.meta.url), "utf8");
	// Connect sends cfg.password to the typed address. The new-machine form must
	// start blank rather than load the active machine's saved credentials.
	expect(config).toMatch(/export const DEFAULT_CONFIG:[\s\S]*?host: ""[\s\S]*?password: ""/);
	expect(sheet).toContain('password: editingHost?.token ?? ""');
	expect(sheet).toContain("const [cfg, setCfg] = useState<ServerConfig>(() => {");
	expect(sheet).not.toMatch(/\bloadConfig\b/);
});
