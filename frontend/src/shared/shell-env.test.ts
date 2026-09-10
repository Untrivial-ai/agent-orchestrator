import { describe, expect, it } from "vitest";
import {
	buildDaemonEnv,
	devDaemonAllowedOrigins,
	FALLBACK_PATH_DIRS,
	parseEnvBlock,
	resolveShellEnv,
	resolveShellPath,
	resolveWindowsShellProbe,
	SHELL_ENV_SENTINEL,
	type ShellRunner,
	withFallbackPath,
} from "./shell-env";

describe("parseEnvBlock", () => {
	it("parses NUL-separated records after the sentinel", () => {
		const stdout = `${SHELL_ENV_SENTINEL}PATH=/opt/homebrew/bin:/usr/bin\0HOME=/Users/me\0`;
		expect(parseEnvBlock(stdout)).toEqual({
			PATH: "/opt/homebrew/bin:/usr/bin",
			HOME: "/Users/me",
		});
	});

	it("drops banner/prompt noise printed before the sentinel", () => {
		const stdout = `motd line\nWelcome\n${SHELL_ENV_SENTINEL}FOO=bar\0`;
		expect(parseEnvBlock(stdout)).toEqual({ FOO: "bar" });
	});

	it("preserves a value containing a newline", () => {
		const stdout = `${SHELL_ENV_SENTINEL}MULTI=line1\nline2\0NEXT=ok\0`;
		expect(parseEnvBlock(stdout)).toEqual({ MULTI: "line1\nline2", NEXT: "ok" });
	});

	it("skips records with no '=' or a leading '='", () => {
		const stdout = `${SHELL_ENV_SENTINEL}NOEQUALS\0=leading\0GOOD=value\0`;
		expect(parseEnvBlock(stdout)).toEqual({ GOOD: "value" });
	});

	it("parses newline-separated records emitted by Windows shells", () => {
		const stdout = `banner\r\n${SHELL_ENV_SENTINEL}\r\nPATH=C:\\Tools;C:\\Windows\r\nFOO=bar\r\n`;
		expect(parseEnvBlock(stdout)).toEqual({ PATH: "C:\\Tools;C:\\Windows", FOO: "bar" });
	});
});

describe("withFallbackPath", () => {
	it("appends only floor dirs not already present, preserving the existing order", () => {
		// /opt/homebrew/bin and /usr/bin are already present and stay put; the rest
		// of the floor is appended in floor order, with no duplicates added.
		const result = withFallbackPath("/opt/homebrew/bin:/custom/bin:/usr/bin");
		expect(result).toBe(
			"/opt/homebrew/bin:/custom/bin:/usr/bin:/opt/homebrew/sbin:/usr/local/bin:/bin:/usr/sbin:/sbin",
		);
	});

	it("yields the full floor for undefined input", () => {
		expect(withFallbackPath(undefined)).toBe(FALLBACK_PATH_DIRS.join(":"));
	});

	it("yields the full floor for empty input", () => {
		expect(withFallbackPath("")).toBe(FALLBACK_PATH_DIRS.join(":"));
	});
});

describe("buildDaemonEnv", () => {
	const minimalProcessEnv: NodeJS.ProcessEnv = { PATH: "/usr/bin:/bin" };

	it("lets overrides win over both shell and process env", () => {
		const env = buildDaemonEnv(
			{ ...minimalProcessEnv, AO_TELEMETRY_EVENTS: "off" },
			{ PATH: "/opt/homebrew/bin", AO_TELEMETRY_EVENTS: "shell" },
			{ AO_TELEMETRY_EVENTS: "on" },
		);
		expect(env.AO_TELEMETRY_EVENTS).toBe("on");
	});

	it("keeps a credential present only in the shell env", () => {
		const env = buildDaemonEnv(minimalProcessEnv, { PATH: "/opt/homebrew/bin", ANTHROPIC_API_KEY: "sk-ant" }, {});
		expect(env.ANTHROPIC_API_KEY).toBe("sk-ant");
	});

	it("takes PATH from the shell env (with floor) over a minimal process PATH", () => {
		const env = buildDaemonEnv(minimalProcessEnv, { PATH: "/opt/homebrew/bin:/usr/bin" }, {});
		expect(env.PATH).toBe("/opt/homebrew/bin:/usr/bin:/opt/homebrew/sbin:/usr/local/bin:/bin:/usr/sbin:/sbin");
	});

	it("still produces a PATH containing the floor when shellEnv is null", () => {
		const env = buildDaemonEnv(minimalProcessEnv, null, {});
		for (const dir of FALLBACK_PATH_DIRS) {
			expect(env.PATH?.split(":")).toContain(dir);
		}
	});

	it("defaults TERM when neither shell nor process env sets it (Finder launch)", () => {
		const env = buildDaemonEnv(minimalProcessEnv, null, {});
		expect(env.TERM).toBe("xterm-256color");
	});

	it("lets a real TERM from the process env win over the default", () => {
		const env = buildDaemonEnv({ ...minimalProcessEnv, TERM: "screen-256color" }, null, {});
		expect(env.TERM).toBe("screen-256color");
	});

	it("replaces TERM=dumb because tmux attach needs clear-screen support", () => {
		const env = buildDaemonEnv({ ...minimalProcessEnv, TERM: "dumb" }, null, {});
		expect(env.TERM).toBe("xterm-256color");
	});

});

describe("devDaemonAllowedOrigins", () => {
	it("adds the exact Vite renderer origin while retaining the packaged renderer origin", () => {
		expect(devDaemonAllowedOrigins(undefined, "http://localhost:5173/settings?section=agents")).toBe(
			"app://renderer,http://localhost:5173",
		);
	});

	it("preserves an explicit operator allowlist", () => {
		expect(devDaemonAllowedOrigins("http://localhost:9999", "http://localhost:5173")).toBe(
			"http://localhost:9999",
		);
	});
});

describe("resolveShellPath", () => {
	it("returns $SHELL when set", () => {
		expect(resolveShellPath({ SHELL: "/bin/bash" })).toBe("/bin/bash");
	});

	it("falls back to /bin/zsh when unset", () => {
		expect(resolveShellPath({})).toBe("/bin/zsh");
	});

	it("falls back to /bin/zsh when blank", () => {
		expect(resolveShellPath({ SHELL: "   " })).toBe("/bin/zsh");
	});
});

describe("resolveShellEnv", () => {
	it("yields the parsed map on a successful probe", async () => {
		const run: ShellRunner = async () => `${SHELL_ENV_SENTINEL}PATH=/opt/homebrew/bin\0FOO=bar\0`;
		expect(await resolveShellEnv({ SHELL: "/bin/zsh" }, run)).toEqual({
			PATH: "/opt/homebrew/bin",
			FOO: "bar",
		});
	});

	it("returns null when the runner returns null", async () => {
		const run: ShellRunner = async () => null;
		expect(await resolveShellEnv({}, run)).toBeNull();
	});

	it("returns null when the runner throws", async () => {
		const run: ShellRunner = async () => {
			throw new Error("spawn failed");
		};
		expect(await resolveShellEnv({}, run)).toBeNull();
	});

	it("returns null when the parsed env lacks PATH", async () => {
		const run: ShellRunner = async () => `${SHELL_ENV_SENTINEL}FOO=bar\0`;
		expect(await resolveShellEnv({}, run)).toBeNull();
	});
});

describe("resolveWindowsShellProbe", () => {
	const lookup = (available: Record<string, string>) => (candidate: string) => available[candidate] ?? null;

	it("resolves Git Bash through the Git installation and login arguments", () => {
		const gitPath = "C:\\Program Files\\Git\\cmd\\git.exe";
		const bashPath = "C:\\Program Files\\Git\\bin\\bash.exe";
		const probe = resolveWindowsShellProbe(
			{ kind: "git-bash" },
			{},
			lookup({ "git.exe": gitPath, [bashPath]: bashPath }),
		);
		expect(probe).toEqual({
			shellPath: bashPath,
			args: ["--login", "-i", "-c", expect.stringContaining(SHELL_ENV_SENTINEL)],
		});
	});

	it("uses ComSpec for an explicit Command Prompt preference", () => {
		const comSpec = "C:\\Windows\\System32\\cmd.exe";
		expect(resolveWindowsShellProbe({ kind: "cmd" }, { ComSpec: comSpec }, lookup({ [comSpec]: comSpec }))).toEqual({
			shellPath: comSpec,
			args: ["/d", "/s", "/c", expect.stringContaining(SHELL_ENV_SENTINEL)],
		});
	});

	it("resolves a custom executable for environment detection", () => {
		const custom = "C:\\Tools\\bash.exe";
		const probe = resolveWindowsShellProbe({ kind: "custom", path: custom }, {}, lookup({ [custom]: custom }));
		expect(probe?.shellPath).toBe(custom);
		expect(probe?.args.slice(0, 2)).toEqual(["--login", "-i"]);
	});
});
