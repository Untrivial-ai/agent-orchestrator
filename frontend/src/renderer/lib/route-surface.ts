/** Coarse route bucket used in user-initiated diagnostics reports. */
export function routeSurface(pathname: string): string {
	if (pathname === "/") return "home";
	if (/^\/settings(?:\/|$)/.test(pathname)) return "global_settings";
	if (/^\/projects\/[^/]+\/sessions\/[^/]+$/.test(pathname)) return "session_detail";
	if (/^\/projects\/[^/]+(?:\/|$)/.test(pathname)) {
		if (/\/settings$/.test(pathname)) return "project_settings";
		return "project_board";
	}
	if (/^\/sessions\/[^/]+$/.test(pathname)) return "session_detail";
	return "other";
}
