import { describe, expect, it } from "vitest";
import { formatCPU, formatMemory } from "./useSessionMemory";
import { formatDuration, processKind } from "../components/SessionMemoryPanel";

describe("formatMemory", () => {
	it("shows whole megabytes and switches to GB at a thousand", () => {
		expect(formatMemory(641_728_512)).toBe("642 MB");
		expect(formatMemory(2_254_857_830)).toBe("2.3 GB");
	});
});

describe("formatCPU", () => {
	it("is a whole percent of one core", () => {
		expect(formatCPU(82.4)).toBe("82%");
		expect(formatCPU(0.34)).toBe("0%");
	});
});

describe("processKind", () => {
	it("names the kind of process, keeping AO's subcommand", () => {
		expect(processKind("/tmp/.mount_x/resources/daemon/ao chat-host s1 /home/u")).toBe("ao chat-host");
		expect(processKind("/usr/bin/git status --short")).toBe("git");
		expect(processKind("sh -c go test ./...")).toBe("sh");
		expect(processKind("")).toBe("?");
	});
});

describe("formatDuration", () => {
	it("reads as seconds, then minutes, then hours", () => {
		expect(formatDuration(38_400)).toBe("38s");
		expect(formatDuration(250_000)).toBe("4m 10s");
		expect(formatDuration(3_720_000)).toBe("1h 2m");
	});
});
