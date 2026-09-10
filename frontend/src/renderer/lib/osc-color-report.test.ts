import { describe, expect, it } from "vitest";
import {
	buildOscColorReports,
	createCursorPositionReportForwarder,
	createOscColorReportForwarder,
	cursorOscProbeRepliesForOutput,
	hexToOscRgb,
	isOscColorReport,
} from "./osc-color-report";

const palette = {
	foreground: "#24292f",
	background: "#f5f5f4",
	cursor: "#24292f",
};

describe("isOscColorReport", () => {
	it("accepts xterm OSC 10/11/12 background reports", () => {
		expect(isOscColorReport("\x1b]11;rgb:f5f5/f5f5/f4f4\x1b\\")).toBe(true);
		expect(isOscColorReport("\x1b]10;rgb:2424/2929/2f2f\x07")).toBe(true);
		expect(isOscColorReport("\x1b]12;rgb:2424/2929/2f2f\x1b\\")).toBe(true);
	});

	it("rejects other terminal-generated replies", () => {
		expect(isOscColorReport("\x1b[?1;2c")).toBe(false);
		expect(isOscColorReport("\x1b[6n")).toBe(false);
		expect(isOscColorReport("\x1b[?997;1n")).toBe(false);
		expect(isOscColorReport("\x1b]0;title\x07")).toBe(false);
	});
});

describe("hexToOscRgb", () => {
	it("expands hex colors into xterm rgb sequences", () => {
		expect(hexToOscRgb("#f5f5f4")).toBe("rgb:f5f5/f5f5/f4f4");
	});
});

describe("buildOscColorReports", () => {
	it("emits fg/bg/cursor OSC replies for the live palette", () => {
		expect(buildOscColorReports(palette)).toBe(
			"\x1b]10;rgb:2424/2929/2f2f\x07\x1b]11;rgb:f5f5/f5f5/f4f4\x07\x1b]12;rgb:2424/2929/2f2f\x07",
		);
	});
});

describe("cursorOscProbeRepliesForOutput", () => {
	it("answers OSC probes seen on the PTY output stream", () => {
		expect(cursorOscProbeRepliesForOutput("\x1b]11;?\x07", palette)).toBe(
			"\x1b]11;rgb:f5f5/f5f5/f4f4\x07",
		);
		expect(cursorOscProbeRepliesForOutput("\x1b]10;?\x1b]11;?\x07", palette)).toBe(
			"\x1b]10;rgb:2424/2929/2f2f\x07\x1b]11;rgb:f5f5/f5f5/f4f4\x07",
		);
		expect(cursorOscProbeRepliesForOutput("hello", palette)).toBeNull();
	});
});

describe("createOscColorReportForwarder", () => {
	it("forwards a complete BEL-terminated OSC 11 report", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push("\x1b]11;rgb:f5f5/f5f5/f4f4\x07");

		expect(forwarded).toEqual(["\x1b]11;rgb:f5f5/f5f5/f4f4\x07"]);
		forwarder.dispose();
	});

	it("forwards concatenated OSC 10 and OSC 11 reports", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push(
			"\x1b]10;rgb:2424/2929/2f2f\x07\x1b]11;rgb:f5f5/f5f5/f4f4\x1b\\",
		);

		expect(forwarded).toEqual([
			"\x1b]10;rgb:2424/2929/2f2f\x07",
			"\x1b]11;rgb:f5f5/f5f5/f4f4\x1b\\",
		]);
		forwarder.dispose();
	});

	it("buffers split OSC replies before forwarding", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));
		forwarder.push("\x1b");
		expect(forwarded).toEqual([]);
		forwarder.push("]11;rgb:f5f5/f5f5/f4f4\x07");
		expect(forwarded).toEqual(["\x1b]11;rgb:f5f5/f5f5/f4f4\x07"]);
		forwarder.dispose();
	});

	it("forwards indexed OSC 4 color reports", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push("\x1b]4;196;rgb:ffff/0000/8000\x1b\\");

		expect(forwarded).toEqual(["\x1b]4;196;rgb:ffff/0000/8000\x1b\\"]);
		forwarder.dispose();
	});

	it("accepts OSC 4 palette boundary indexes", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push("\x1b]4;0;rgb:0000/0000/0000\x07\x1b]4;255;rgb:ffff/ffff/ffff\x1b\\");

		expect(forwarded).toEqual([
			"\x1b]4;0;rgb:0000/0000/0000\x07",
			"\x1b]4;255;rgb:ffff/ffff/ffff\x1b\\",
		]);
		forwarder.dispose();
	});

	it("rejects OSC 4 palette indexes above 255", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push("\x1b]4;256;rgb:ffff/ffff/ffff\x07");

		expect(forwarded).toEqual([]);
		forwarder.dispose();
	});

	it("recovers after oversized unterminated input", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push(`\x1b]11;${"x".repeat(20_000)}`);
		forwarder.push("\x1b]11;rgb:f5f5/f5f5/f4f4\x07");

		expect(forwarded).toEqual(["\x1b]11;rgb:f5f5/f5f5/f4f4\x07"]);
		forwarder.dispose();
	});

	it("rejects generic xterm data and malformed color reports", () => {
		const forwarded: string[] = [];
		const forwarder = createOscColorReportForwarder((report) => forwarded.push(report));

		forwarder.push("\x1b[A");
		forwarder.push("\x1b]11;rgb:f5f5/f5f5/not-a-color\x07");

		expect(forwarded).toEqual([]);
		forwarder.dispose();
	});
});

describe("createCursorPositionReportForwarder", () => {
	it("forwards standard and private cursor-position replies", () => {
		const forwarded: string[] = [];
		const forwarder = createCursorPositionReportForwarder((report) => forwarded.push(report));

		forwarder.observeOutput("\x1b[6n\x1b[?6n");
		forwarder.push("\x1b[12;34R\x1b[?5;9R");

		expect(forwarded).toEqual(["\x1b[12;34R", "\x1b[?5;9R"]);
	});

	it("buffers a split cursor-position reply", () => {
		const forwarded: string[] = [];
		const forwarder = createCursorPositionReportForwarder((report) => forwarded.push(report));

		forwarder.observeOutput("\x1b[");
		forwarder.observeOutput("6n");
		forwarder.push("\x1b[12;");
		expect(forwarded).toEqual([]);
		forwarder.push("34R");

		expect(forwarded).toEqual(["\x1b[12;34R"]);
	});

	it("rejects unsolicited, keyboard, and malformed control data", () => {
		const forwarded: string[] = [];
		const forwarder = createCursorPositionReportForwarder((report) => forwarded.push(report));

		forwarder.push("\x1b[12;34Ry\r\x1b[A\x1b[0;2R\x1b[2;3H");

		expect(forwarded).toEqual([]);
	});
});
