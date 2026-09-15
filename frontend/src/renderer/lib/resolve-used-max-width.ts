/**
 * Resolve an element's used `max-width` in CSS pixels.
 *
 * DO NOT use bare `Number.parseFloat(getComputedStyle(el).maxWidth)`:
 * when max-width is `min()` / `max()` / `clamp()` (inspector's
 * `--session-inspector-max-width`), engines often return the unresolved
 * expression string. parseFloat then yields NaN and callers fall back to a
 * loose prop max (e.g. `defaultWidth * 2`) — the inspector grip/panel can be
 * dragged past the painted leftmost limit while the min (rightmost) clamp
 * still looks fine.
 */
export function resolveUsedMaxWidthPx(element: HTMLElement): number | null {
	const raw = getComputedStyle(element).maxWidth.trim();
	if (!raw || raw === "none") return null;
	if (raw.endsWith("px")) {
		const px = Number.parseFloat(raw);
		return Number.isFinite(px) && px > 0 ? px : null;
	}

	// Unresolved expression: measure a probe in the percentage containing block.
	const container =
		(element.offsetParent instanceof HTMLElement ? element.offsetParent : null) ??
		element.closest("#session-workspace") ??
		element.parentElement;
	if (!container) return null;

	const probe = document.createElement("div");
	probe.setAttribute("data-max-width-probe", "");
	probe.style.cssText = [
		"position:absolute",
		"visibility:hidden",
		"pointer-events:none",
		"top:0",
		"left:0",
		"height:0",
		"width:99999px",
		`max-width:${raw}`,
	].join(";");
	container.appendChild(probe);
	const width = probe.getBoundingClientRect().width;
	probe.remove();
	return Number.isFinite(width) && width > 0 ? width : null;
}
