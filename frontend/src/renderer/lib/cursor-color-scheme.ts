// Cursor Agent registers DEC mode 2031 and listens on stdin for ESC [ ? 997 ; 1 n
// (dark) or 997 ; 2 n (light). It may also emit those sequences on stdout as
// probes; AO must answer on stdin with the live scheme notification.

export type ColorScheme = "light" | "dark";

const CURSOR_COLOR_SCHEME_MODE_ON = "\x1b[?2031h";
const CURSOR_COLOR_SCHEME_QUERY = /\x1b\[\?997;[12]n|\[\?997;[12]n/g;

export function buildCursorColorSchemeNotification(scheme: ColorScheme): string {
	return scheme === "light" ? "\x1b[?997;2n" : "\x1b[?997;1n";
}

export function cursorColorSchemeReplyForOutput(chunk: string, theme: ColorScheme): string | null {
	if (chunk.includes(CURSOR_COLOR_SCHEME_MODE_ON) || cursorColorSchemeQueryInOutput(chunk)) {
		return buildCursorColorSchemeNotification(theme);
	}
	return null;
}

function cursorColorSchemeQueryInOutput(data: string): boolean {
	CURSOR_COLOR_SCHEME_QUERY.lastIndex = 0;
	return CURSOR_COLOR_SCHEME_QUERY.test(data);
}
