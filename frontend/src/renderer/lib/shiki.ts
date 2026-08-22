import { codeToHtml, type BundledLanguage, type BundledTheme } from "shiki";

// Covers the languages an agent's repo is most likely to contain. Anything
// else falls back to "plaintext" — Shiki renders it as plain themed text
// instead of throwing on an unrecognized grammar.
const LANGUAGE_BY_EXTENSION: Record<string, BundledLanguage> = {
	ts: "typescript",
	mts: "typescript",
	cts: "typescript",
	tsx: "tsx",
	js: "javascript",
	mjs: "javascript",
	cjs: "javascript",
	jsx: "jsx",
	go: "go",
	py: "python",
	rb: "ruby",
	rs: "rust",
	java: "java",
	kt: "kotlin",
	kts: "kotlin",
	swift: "swift",
	c: "c",
	h: "c",
	cc: "cpp",
	cpp: "cpp",
	cxx: "cpp",
	hpp: "cpp",
	cs: "csharp",
	php: "php",
	sh: "bash",
	bash: "bash",
	zsh: "bash",
	json: "json",
	jsonc: "jsonc",
	yaml: "yaml",
	yml: "yaml",
	toml: "toml",
	md: "markdown",
	mdx: "mdx",
	html: "html",
	css: "css",
	scss: "scss",
	less: "less",
	sql: "sql",
	graphql: "graphql",
	dockerfile: "docker",
	proto: "proto",
	xml: "xml",
	vue: "vue",
	svelte: "svelte",
	lua: "lua",
	diff: "diff",
};

export function languageForPath(path: string): BundledLanguage | "plaintext" {
	const base = path.split("/").pop() ?? path;
	if (/^dockerfile$/i.test(base)) return "docker";
	if (/^makefile$/i.test(base)) return "makefile" as BundledLanguage;
	const dot = base.lastIndexOf(".");
	if (dot < 0) return "plaintext";
	const ext = base.slice(dot + 1).toLowerCase();
	return LANGUAGE_BY_EXTENSION[ext] ?? "plaintext";
}

const THEME_BY_MODE: Record<"light" | "dark", BundledTheme> = {
	light: "github-light",
	dark: "github-dark",
};

export function highlightToHtml(content: string, lang: BundledLanguage | "plaintext", mode: "light" | "dark"): Promise<string> {
	return codeToHtml(content, { lang, theme: THEME_BY_MODE[mode] });
}
