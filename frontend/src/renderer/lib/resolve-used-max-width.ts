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

/** Matches SessionView's INSPECTOR_SEPARATOR_RESERVE_PX for drag-limit parity. */
const INSPECTOR_SEPARATOR_RESERVE_PX = 8;

/**
 * Parse `#session-workspace`'s `--session-inspector-max-width`
 * (`min(P%, max(Aminpx, calc(100% - Chatpx)))`) against the split width.
 * Same arithmetic as `inspectorMaxWidthPx` — deterministic, no getComputedStyle.
 */
export function resolveSessionInspectorMaxWidthPx(from: HTMLElement): number | null {
	const split =
		from.id === "session-workspace" ? from : from.closest("#session-workspace");
	if (!(split instanceof HTMLElement)) return null;

	const available = Math.max(0, split.clientWidth - INSPECTOR_SEPARATOR_RESERVE_PX);
	if (available <= 0) return null;

	const raw = (
		split.style.getPropertyValue("--session-inspector-max-width") ||
		getComputedStyle(split).getPropertyValue("--session-inspector-max-width")
	).trim();
	if (!raw) return available;

	const percent = Number(raw.match(/min\(\s*([\d.]+)\s*%/i)?.[1]);
	const absMin = Number(raw.match(/max\(\s*([\d.]+)\s*px/i)?.[1]);
	const chatMin = Number(raw.match(/100%\s*-\s*([\d.]+)\s*px/i)?.[1]);
	if (!Number.isFinite(percent)) return available;

	const percentageCap = Math.floor((available * percent) / 100);
	const readableCap = Math.max(
		Number.isFinite(absMin) ? absMin : 0,
		available - (Number.isFinite(chatMin) ? chatMin : 0),
	);
	return Math.min(available, percentageCap, readableCap);
}

export function resolveUsedMaxWidthPx(element: HTMLElement): number | null {
	const raw = getComputedStyle(element).maxWidth.trim();
	if (!raw || raw === "none") {
		// Inspector panels use max-w-(--session-inspector-max-width); if the used
		// value is somehow none, still honor the split CSS variable.
		return resolveSessionInspectorMaxWidthPx(element);
	}
	if (raw.endsWith("px")) {
		const px = Number.parseFloat(raw);
		if (Number.isFinite(px) && px > 0) {
			const fromSplit = resolveSessionInspectorMaxWidthPx(element);
			return fromSplit !== null ? Math.min(px, fromSplit) : px;
		}
	}

	// Unresolved min()/max()/clamp(): prefer the split formula (matches paint).
	const fromSplit = resolveSessionInspectorMaxWidthPx(element);
	if (fromSplit !== null) return fromSplit;

	// Last resort probe in the percentage containing block.
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
