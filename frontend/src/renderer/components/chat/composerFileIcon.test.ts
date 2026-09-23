import { Code2, File, FileJson2, FileText, Image, Video } from "lucide-react";
import { describe, expect, it } from "vitest";
import { composerFileIcon } from "./composerFileIcon";

describe("composerFileIcon", () => {
	it.each([
		["src/app.tsx", Code2],
		["scripts/check.py", Code2],
		["package.json", FileJson2],
		["docs/notes.md", FileText],
		["assets/preview.png", Image],
		["demo.mp4", Video],
		["LICENSE", File],
	])("maps %s to a minimal file-type icon", (path, Icon) => {
		expect(composerFileIcon(path)).toBe(Icon);
	});
});
