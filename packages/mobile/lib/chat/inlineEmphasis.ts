/** Underscores inside identifiers are text, not Markdown emphasis delimiters. */
export function allowsUnderscoreEmphasis(text: string, start: number, end: number): boolean {
	return !/\w/.test(text[start - 1] ?? "") && !/\w/.test(text[end] ?? "");
}
