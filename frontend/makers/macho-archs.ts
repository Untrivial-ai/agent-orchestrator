import { closeSync, fstatSync, openSync, readSync } from "node:fs";

// Mach-O header constants. Universal ("fat") binaries start with FAT_MAGIC /
// FAT_MAGIC_64 stored big-endian; thin Mach-O images start with MH_MAGIC /
// MH_MAGIC_64 stored in the writer's byte order, so the endianness is derived
// from the magic itself, never from the running host.
const FAT_MAGIC = 0xcafebabe;
const FAT_MAGIC_64 = 0xcafebabf;
const MH_MAGIC = 0xfeedface;
const MH_MAGIC_64 = 0xfeedfacf;
// Both supported slices are 64-bit ABIs. Classify by cputype only, never by
// cpusubtype: `lipo -archs` prints "arm64e" for cpusubtype 0x80000002, but the
// cputype is plain arm64, and neither slice needs the x64 executable-memory
// entitlement this module gates. The JS helper and the verifier's `lipo -archs`
// agree on the one invariant that matters: "contains an x86_64 slice".
const CPU_TYPE_X86_64 = 0x01000007;
const CPU_TYPE_ARM64 = 0x0100000c;
// Real universal binaries carry 1-2 slices. The bound also rejects Java class
// files, whose magic is the same 0xCAFEBABE.
const MAX_FAT_ARCHES = 16;
// Largest header we ever read: fat magic + nfat_arch + MAX_FAT_ARCHES 32-byte
// fat_64 entries. The bundled ACP Node alone is ~113 MB; this keeps the parse
// a prefix read instead of loading the binary.
const MAX_HEADER_BYTES = 8 + MAX_FAT_ARCHES * 32;

export type MachOArch = "x86_64" | "arm64";

// Thrown for any file that cannot be read or whose header is not a well-formed
// Mach-O (thin or fat). Signing callers must let it propagate: failing the
// signing pass is the contract, not silently falling back to a guess.
export class MachOParseError extends Error {
	constructor(
		message: string,
		readonly filePath: string,
	) {
		super(`${message}: ${filePath}`);
		this.name = "MachOParseError";
	}
}

// Architectures present in the file, one entry per slice in slice order,
// de-duplicated. Reads at most MAX_HEADER_BYTES of the file, never the whole
// binary.
export function machoArchs(filePath: string): MachOArch[] {
	let fd: number;
	try {
		fd = openSync(filePath, "r");
	} catch (error) {
		throw new MachOParseError(`cannot open (${(error as NodeJS.ErrnoException)?.code ?? error})`, filePath);
	}
	try {
		const stats = fstatSync(fd);
		if (!stats.isFile()) {
			throw new MachOParseError("not a regular file", filePath);
		}
		// readSync may legally return fewer bytes than requested even for a
		// regular file, so accumulate until the header is complete or EOF.
		const header = Buffer.alloc(MAX_HEADER_BYTES);
		let total = 0;
		while (total < header.length) {
			const read = readSync(fd, header, total, header.length - total, total);
			if (read === 0) break;
			total += read;
		}
		if (total < 8) {
			throw new MachOParseError("truncated Mach-O header", filePath);
		}
		const magicBE = header.readUInt32BE(0);
		const magicLE = header.readUInt32LE(0);
		if (magicBE === FAT_MAGIC || magicBE === FAT_MAGIC_64) {
			const stride = magicBE === FAT_MAGIC_64 ? 32 : 20;
			const nfatArch = header.readUInt32BE(4);
			if (nfatArch === 0 || nfatArch > MAX_FAT_ARCHES) {
				throw new MachOParseError(`invalid fat header (nfat_arch ${nfatArch})`, filePath);
			}
			if (8 + nfatArch * stride > total) {
				throw new MachOParseError("truncated fat header", filePath);
			}
			const archs: MachOArch[] = [];
			for (let index = 0; index < nfatArch; index += 1) {
				const arch = classify(header.readUInt32BE(8 + index * stride), filePath);
				if (!archs.includes(arch)) archs.push(arch);
			}
			return archs;
		}
		if (magicLE === MH_MAGIC_64 || magicLE === MH_MAGIC) {
			return [classify(header.readUInt32LE(4), filePath)];
		}
		// Byte-swapped thin header: written by a host whose byte order differs
		// from this machine's.
		if (magicBE === MH_MAGIC_64 || magicBE === MH_MAGIC) {
			return [classify(header.readUInt32BE(4), filePath)];
		}
		throw new MachOParseError(`not a Mach-O image (magic 0x${magicBE.toString(16).padStart(8, "0")})`, filePath);
	} finally {
		closeSync(fd);
	}
}

// The one predicate the signing policy needs: does the binary contain an
// x86_64 slice (a thin x64 binary, or a universal carrying an x64 slice)?
export function machoHasX86_64Slice(filePath: string): boolean {
	return machoArchs(filePath).includes("x86_64");
}

function classify(cputype: number, filePath: string): MachOArch {
	if (cputype === CPU_TYPE_X86_64) return "x86_64";
	if (cputype === CPU_TYPE_ARM64) return "arm64";
	throw new MachOParseError(`unsupported cputype 0x${cputype.toString(16)}`, filePath);
}
