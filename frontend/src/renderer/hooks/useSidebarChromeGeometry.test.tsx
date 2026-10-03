import { renderHook } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { useSidebarChromeGeometry } from "./useSidebarChromeGeometry";

vi.mock("../lib/platform", () => ({ isMacPlatform: () => true }));

it("observes the new desktop elements after a responsive remount", () => {
	const gapRef = { current: null as HTMLDivElement | null };
	const containerRef = { current: null as HTMLDivElement | null };
	const { rerender, unmount } = renderHook(({ ready }) => useSidebarChromeGeometry(ready, gapRef, containerRef), { initialProps: { ready: false } });
	const host = document.createElement("div");
	host.innerHTML = '<div data-slot="sidebar-gap"></div><div data-slot="sidebar-container"></div>';
	document.body.append(host);
	const [gap, container] = host.children;
	gapRef.current = gap as HTMLDivElement;
	containerRef.current = container as HTMLDivElement;
	vi.spyOn(gap, "getBoundingClientRect").mockReturnValue({ width: 120 } as DOMRect);
	vi.spyOn(container, "getBoundingClientRect").mockReturnValue({ width: 240 } as DOMRect);
	rerender({ ready: true });
	expect(document.documentElement.style.getPropertyValue("--ao-sidebar-layout-width")).toBe("120px");
	expect(document.documentElement.style.getPropertyValue("--ao-sidebar-collapse-progress")).toBe("0.5");
	unmount();
	expect(document.documentElement.style.getPropertyValue("--ao-sidebar-collapse-progress")).toBe("");
	host.remove();
});
