"use client";

import Link from "next/link";
import type { ComponentPropsWithoutRef, MouseEvent } from "react";

type LinkProps = ComponentPropsWithoutRef<typeof Link>;

/**
 * `<Link>` for in-page anchors that never leaves the fragment in the URL.
 *
 * A plain `<Link href="/#see-it">` sets `#see-it` and scrolls, but the hash then
 * sits in the address bar forever — scroll away, click the same link again, and
 * the browser considers the target current and does nothing. Here a same-page
 * click is handled directly: scroll the element into view and let the URL stay
 * clean, so the link works identically on every click.
 *
 * Cross-page navigation (`/docs` -> `/#see-it`) falls through to normal `Link`
 * behaviour, and so does anything without a resolvable target — the anchor still
 * degrades to a real navigation with JS off.
 */
export function HashLink({ href, onClick, ...props }: LinkProps) {
	const raw = typeof href === "string" ? href : null;
	const hashIndex = raw?.indexOf("#") ?? -1;
	const targetId = hashIndex >= 0 ? raw?.slice(hashIndex + 1) : undefined;
	const targetPath = hashIndex >= 0 ? raw?.slice(0, hashIndex) || "/" : undefined;

	const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
		onClick?.(event);

		if (event.defaultPrevented) return;
		// Let the browser own modified clicks (new tab, download, middle click).
		if (
			event.button !== 0 ||
			event.metaKey ||
			event.ctrlKey ||
			event.shiftKey ||
			event.altKey
		) {
			return;
		}
		if (!targetId || targetPath !== window.location.pathname) return;

		const target = document.getElementById(targetId);
		if (!target) return;

		event.preventDefault();
		// Deferred a frame so an `onClick` that unlocks the page first — the mobile
		// nav restores `body { overflow }` on close — has committed; scrolling a
		// still-locked body silently does nothing.
		requestAnimationFrame(() => {
			// No behaviour override: `scroll-behavior` / `scroll-padding-top` on
			// <html> supply the smoothness and the header offset, and honour the
			// reduced-motion preference.
			target.scrollIntoView();
		});
	};

	return <Link href={href} onClick={handleClick} {...props} />;
}
