import { MAC_TITLEBAR_HEIGHT } from "../../shared/window-chrome";
import { useEffect, useLayoutEffect, useState } from "react";
import { aoBridge } from "../lib/bridge";
import { isMacPlatform } from "../lib/platform";

export function useWindowZoomFactor(): number {
	const [zoomFactor, setZoomFactor] = useState(1);
	useEffect(() => {
		if (!isMacPlatform()) return;
		let live = true;
		let version = 0;
		const off = aoBridge.window.onZoomFactor((value) => {
			version += 1;
			setZoomFactor(value);
		});
		// Native menu zoom roles do not emit zoom-changed. Chromium does resize
		// the CSS viewport, so refresh the native placement and reserve here too.
		const refresh = () => {
			const requestVersion = ++version;
			void aoBridge.window.getZoomFactor().then((value) => {
				if (live && version === requestVersion) setZoomFactor(value);
			});
		};
		refresh();
		window.addEventListener("resize", refresh);
		return () => {
			live = false;
			off();
			window.removeEventListener("resize", refresh);
		};
	}, []);
	// Set tokens on their declaration scope: inherited custom properties have
	// already resolved var() references, so a shell-only override cannot update
	// the dependent native-control/content clearances (including body portals).
	useLayoutEffect(() => {
		if (!isMacPlatform()) return;
		const style = document.documentElement.style;
		const values = {
			"--size-topbar-primary": `${MAC_TITLEBAR_HEIGHT}px`,
			"--size-topbar-secondary": `${MAC_TITLEBAR_HEIGHT}px`,
			"--ao-window-zoom": String(zoomFactor),
		};
		const previous = Object.keys(values).map((name) => [name, style.getPropertyValue(name)]);
		for (const [name, value] of Object.entries(values)) style.setProperty(name, value);
		return () => {
			for (const [name, value] of previous) {
				if (value) style.setProperty(name, value);
				else style.removeProperty(name);
			}
		};
	}, [zoomFactor]);
	return zoomFactor;
}
