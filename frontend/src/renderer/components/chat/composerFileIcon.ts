import { Code2, File, FileJson2, FileText, Image, Video, type LucideIcon } from "lucide-react";

const CODE_EXTENSIONS = new Set([
	"c",
	"cc",
	"cpp",
	"css",
	"go",
	"h",
	"hpp",
	"html",
	"java",
	"js",
	"jsx",
	"kt",
	"lua",
	"php",
	"py",
	"rb",
	"rs",
	"sh",
	"sql",
	"swift",
	"ts",
	"tsx",
	"vue",
	"xml",
	"yaml",
	"yml",
]);
const DOCUMENT_EXTENSIONS = new Set(["csv", "log", "md", "mdx", "rtf", "text", "txt"]);
const IMAGE_EXTENSIONS = new Set(["avif", "gif", "jpeg", "jpg", "png", "svg", "webp"]);
const VIDEO_EXTENSIONS = new Set(["avi", "m4v", "mkv", "mov", "mp4", "webm"]);

export function composerFileIcon(path: string): LucideIcon {
	const extension = path.split(".").pop()?.toLowerCase() ?? "";
	if (CODE_EXTENSIONS.has(extension)) return Code2;
	if (extension === "json") return FileJson2;
	if (DOCUMENT_EXTENSIONS.has(extension)) return FileText;
	if (IMAGE_EXTENSIONS.has(extension)) return Image;
	if (VIDEO_EXTENSIONS.has(extension)) return Video;
	return File;
}
