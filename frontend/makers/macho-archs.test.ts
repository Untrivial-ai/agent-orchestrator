import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { MachOParseError, machoArchs, machoHasX86_64Slice } from "./macho-archs";

// Fixtures are synthesized buffers with the exact byte layouts observed on
// disk (thin arm64: cf fa ed fe | 0c 00 00 01; fat: ca fe ba be | 00 00 00 02
// with 20-byte entries), so no binary fixtures are committed and the suite
// runs on any platform.
const CPU_TYPE_X86_64 = 0x01000007;
const CPU_TYPE_ARM64 = 0x0100000c;

let fixtureDir: string;

function writeFixture(name: string, contents: Buffer): string {
	const filePath = join(fixtureDir, name);
	writeFileSync(filePath, contents);
	return filePath;
}

// Thin header: magic, cputype, cpusubtype, filetype — all 32-bit fields in the
// writer's byte order. Endianness of the fixture is decided by `bigEndian`,
// which is a property of the file, not of the host running the test.
function thinMachO(cputype: number, options: { bigEndian?: boolean; subtype?: number } = {}): Buffer {
	const buffer = Buffer.alloc(16);
	if (options.bigEndian) {
		buffer.writeUInt32BE(0xfeedfacf, 0);
		buffer.writeUInt32BE(cputype, 4);
		buffer.writeUInt32BE(options.subtype ?? 0, 8);
	} else {
		buffer.writeUInt32LE(0xfeedfacf, 0);
		buffer.writeUInt32LE(cputype, 4);
		buffer.writeUInt32LE(options.subtype ?? 0, 8);
	}
	return buffer;
}

// Fat header: magic + nfat_arch big-endian, then 20-byte (FAT_MAGIC) or
// 32-byte (FAT_MAGIC_64) fat_arch entries whose first field is the cputype.
function fatMachO(
	entries: number[],
	options: { is64?: boolean; nfatArchOverride?: number } = {},
): Buffer {
	const magic = options.is64 ? 0xcafebabf : 0xcafebabe;
	const stride = options.is64 ? 32 : 20;
	const nfatArch = options.nfatArchOverride ?? entries.length;
	const buffer = Buffer.alloc(8 + entries.length * stride);
	buffer.writeUInt32BE(magic, 0);
	buffer.writeUInt32BE(nfatArch, 4);
	entries.forEach((cputype, index) => {
		buffer.writeUInt32BE(cputype, 8 + index * stride);
	});
	return buffer;
}

// process.arch is an own accessor on process; pin it per call and always
// restore, so the "never consult the host architecture" contract is proven
// by construction rather than by a comment.
function withHostArch<T>(arch: string, run: () => T): T {
	const descriptor = Object.getOwnPropertyDescriptor(process, "arch");
	Object.defineProperty(process, "arch", { get: () => arch, configurable: true });
	try {
		return run();
	} finally {
		if (descriptor) Object.defineProperty(process, "arch", descriptor);
	}
}

beforeEach(() => {
	fixtureDir = mkdtempSync(join(tmpdir(), "macho-archs-"));
});

afterEach(() => {
	rmSync(fixtureDir, { recursive: true, force: true });
	vi.restoreAllMocks();
});

describe("machoArchs", () => {
	it("reads a thin x86_64 image", () => {
		expect(machoArchs(writeFixture("thin-x64", thinMachO(CPU_TYPE_X86_64)))).toEqual(["x86_64"]);
	});

	it("reads a thin arm64 image", () => {
		expect(machoArchs(writeFixture("thin-arm64", thinMachO(CPU_TYPE_ARM64)))).toEqual(["arm64"]);
	});

	it("classifies an arm64e slice as arm64 (cputype, not cpusubtype)", () => {
		// lipo -archs prints "arm64e" for cpusubtype 0x80000002; the cputype is
		// plain arm64. The policy only asks for x86_64 slices, so the names the
		// two implementations print may differ — this pins the intentional
		// divergence so nobody "fixes" it.
		expect(
			machoArchs(writeFixture("thin-arm64e", thinMachO(CPU_TYPE_ARM64, { subtype: 0x80000002 }))),
		).toEqual(["arm64"]);
	});

	it("derives endianness from the magic, not the host", () => {
		const bigEndian = writeFixture("thin-be", thinMachO(CPU_TYPE_ARM64, { bigEndian: true }));
		expect(machoArchs(bigEndian)).toEqual(["arm64"]);
	});

	it("reads a universal binary with x86_64 and arm64 slices", () => {
		expect(machoArchs(writeFixture("fat", fatMachO([CPU_TYPE_X86_64, CPU_TYPE_ARM64])))).toEqual([
			"x86_64",
			"arm64",
		]);
	});

	it("reads an arm64-only universal binary written as FAT_MAGIC_64", () => {
		expect(machoArchs(writeFixture("fat64", fatMachO([CPU_TYPE_ARM64], { is64: true })))).toEqual([
			"arm64",
		]);
	});

	it("does not mask a 32-bit i386 cputype into x86_64", () => {
		expect(() => machoArchs(writeFixture("i386", thinMachO(0x00000007)))).toThrow(MachOParseError);
	});

	it("rejects an unknown cputype", () => {
		expect(() => machoArchs(writeFixture("unknown", thinMachO(0x0100000b)))).toThrow(
			/unsupported cputype/,
		);
	});

	it("rejects a truncated thin header", () => {
		expect(() => machoArchs(writeFixture("short", Buffer.from([0xcf, 0xfa, 0xed, 0xfe])))).toThrow(
			MachOParseError,
		);
	});

	it("rejects a fat header whose slices run past the end of the file", () => {
		expect(
			() => machoArchs(writeFixture("fat-short", fatMachO([CPU_TYPE_X86_64], { nfatArchOverride: 3 }))),
		).toThrow(/truncated fat header/);
	});

	it("rejects nfat_arch values that cannot describe a real binary", () => {
		expect(() => machoArchs(writeFixture("fat-zero", fatMachO([], { nfatArchOverride: 0 })))).toThrow(
			/invalid fat header/,
		);
		expect(() =>
			machoArchs(writeFixture("fat-huge", fatMachO([CPU_TYPE_X86_64], { nfatArchOverride: 9999 }))),
		).toThrow(/invalid fat header/);
	});

	it.each([
		{ name: "pe", contents: Buffer.from("MZ\x90\x00\x03\x00\x00\x00\x04\x00", "binary") },
		{ name: "elf", contents: Buffer.from("\x7fELF\x02\x01\x01\x00", "binary") },
		{ name: "text", contents: Buffer.from("definitely not a binary") },
	])("rejects non-Mach-O magic ($name)", ({ contents }) => {
		expect(() => machoArchs(writeFixture("not-macho", contents))).toThrow(MachOParseError);
	});

	it("rejects a missing file and a directory", () => {
		expect(() => machoArchs(join(fixtureDir, "does-not-exist"))).toThrow(MachOParseError);
		expect(() => machoArchs(fixtureDir)).toThrow(MachOParseError);
	});

	it("never consults the host architecture", () => {
		const fixture = writeFixture("host-independent", thinMachO(CPU_TYPE_X86_64));
		const fromX64Host = withHostArch("x64", () => machoArchs(fixture));
		const fromArm64Host = withHostArch("arm64", () => machoArchs(fixture));
		const fromAbsurdHost = withHostArch("ppc64", () => machoArchs(fixture));
		expect(fromX64Host).toEqual(["x86_64"]);
		expect(fromArm64Host).toEqual(["x86_64"]);
		expect(fromAbsurdHost).toEqual(["x86_64"]);
	});

	it("cross-checks against a real Mach-O on macOS", () => {
		if (process.platform !== "darwin") return;
		const expected = process.arch === "x64" ? "x86_64" : "arm64";
		expect(machoArchs(process.execPath)).toContain(expected);
	});
});

describe("machoHasX86_64Slice", () => {
	it("is true for a thin x86_64 binary and a universal carrying x86_64", () => {
		expect(machoHasX86_64Slice(writeFixture("q-thin-x64", thinMachO(CPU_TYPE_X86_64)))).toBe(true);
		expect(
			machoHasX86_64Slice(writeFixture("q-fat-x64", fatMachO([CPU_TYPE_X86_64, CPU_TYPE_ARM64]))),
		).toBe(true);
	});

	it("is false for an arm64-only binary", () => {
		expect(machoHasX86_64Slice(writeFixture("q-thin-arm64", thinMachO(CPU_TYPE_ARM64)))).toBe(false);
		expect(machoHasX86_64Slice(writeFixture("q-fat64-arm64", fatMachO([CPU_TYPE_ARM64], { is64: true })))).toBe(
			false,
		);
	});
});
